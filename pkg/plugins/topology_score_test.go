package plugins

import (
	"context"
	"strconv"
	"testing"

	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	sched_framework "k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"

	"github.com/fei/elastic-topo-scheduler/pkg/topology"
)

// topoNode 构造带假拓扑标签的节点(Phase 0 模拟;Phase 1 换真值)。
func topoNode(name, numa string, gpuCount int) *v1.Node {
	return st.MakeNode().Name(name).
		Label(topology.LabelNUMADomain, numa).
		Label(topology.LabelGPUCount, strconv.Itoa(gpuCount)).
		Label(topology.LabelGPUModel, "4090").
		Obj()
}

func TestGroupFromPod(t *testing.T) {
	t.Run("有组标签", func(t *testing.T) {
		p := st.MakePod().Name("p").Label(topology.LabelGroupName, "g1").Label(topology.LabelGroupSize, "4").Obj()
		g, err := groupFromPod(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if g.Name != "g1" || g.Size != 4 {
			t.Errorf("group = %+v, want {g1 4}", g)
		}
	})
	t.Run("无标签退化单组", func(t *testing.T) {
		p := st.MakePod().Name("p").Obj()
		g, err := groupFromPod(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if g.Size != 1 || g.Name == "" {
			t.Errorf("group = %+v, want single-pod group", g)
		}
	})
	t.Run("非法组规模报错", func(t *testing.T) {
		p := st.MakePod().Name("p").Label(topology.LabelGroupSize, "abc").Obj()
		if _, err := groupFromPod(p); err == nil {
			t.Fatal("expected error for invalid group.size")
		}
	})
}

// TestScore_ProximityOutranksEmptyNode 用真实调度框架跑 Score 阶段:
// node0 已驻留一个同组 Pod(邻近度 1/4),node1 全空(邻近度 0)。
// 待调度 Pod 属于组 g1(规模 4)。验证:
//   1. 插件从框架 NodeInfo 正确读到同组驻留 → 纯函数算出更高分
//   2. 纯函数逻辑与框架翻译层连通
func TestScore_ProximityOutranksEmptyNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node0 := topoNode("node0", "numa0", 2)
	node1 := topoNode("node1", "numa1", 2)

	// 已驻留:node0 上有一个 g1 组 Pod(绑到 node0,快照把它归到 node0)
	existing := st.MakePod().Name("g1-p1").Namespace("ns").Label(topology.LabelGroupName, "g1").Obj()
	existing.Spec.NodeName = "node0"

	snapshot := cache.NewSnapshot([]*v1.Pod{existing}, []*v1.Node{node0, node1})
	fh, err := runtime.NewFramework(ctx, nil, nil, runtime.WithSnapshotSharedLister(snapshot))
	if err != nil {
		t.Fatalf("build framework: %v", err)
	}

	// NewTopologyScore 声明返回 fwk.Plugin(工厂形态),测试里断言回具体类型
	raw, err := NewTopologyScore(ctx, nil, fh)
	if err != nil {
		t.Fatalf("build plugin: %v", err)
	}
	pl := raw.(*TopologyScore)

	state := sched_framework.NewCycleState()
	pod := st.MakePod().Name("g1-new").Label(topology.LabelGroupName, "g1").Label(topology.LabelGroupSize, "4").Obj()

	// 从快照取 NodeInfo(而不是从 node 重新构造),保证读到真实的 Pod 分布
	ni0, err := snapshot.Get("node0")
	if err != nil {
		t.Fatalf("get node0 info: %v", err)
	}
	ni1, err := snapshot.Get("node1")
	if err != nil {
		t.Fatalf("get node1 info: %v", err)
	}

	s0, st0 := pl.Score(ctx, state, pod, ni0)
	if !st0.IsSuccess() {
		t.Fatalf("score node0 failed: %v", st0.Message())
	}
	s1, st1 := pl.Score(ctx, state, pod, ni1)
	if !st1.IsSuccess() {
		t.Fatalf("score node1 failed: %v", st1.Message())
	}

	// node0 邻近度 1/4=0.25 > node1 0 → 分数必须更高
	if !(s0 > s1) {
		t.Errorf("node0 with existing group pod should outscore empty node1: got %d vs %d", s0, s1)
	}
}

// TestScore_BothEmpty_Equal 两个空节点分数应相等(同拓扑域,无驻留差)。
func TestScore_BothEmpty_Equal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node0 := topoNode("node0", "numa0", 2)
	node1 := topoNode("node1", "numa1", 2)

	snapshot := cache.NewSnapshot(nil, []*v1.Node{node0, node1})
	fh, _ := runtime.NewFramework(ctx, nil, nil, runtime.WithSnapshotSharedLister(snapshot))
	raw, _ := NewTopologyScore(ctx, nil, fh)
	pl := raw.(*TopologyScore)
	state := sched_framework.NewCycleState()
	pod := st.MakePod().Name("g1-new").Label(topology.LabelGroupName, "g1").Label(topology.LabelGroupSize, "4").Obj()

	ni0, _ := snapshot.Get("node0")
	ni1, _ := snapshot.Get("node1")
	s0, _ := pl.Score(ctx, state, pod, ni0)
	s1, _ := pl.Score(ctx, state, pod, ni1)
	if s0 != s1 {
		t.Errorf("both empty nodes should score equal: got %d vs %d", s0, s1)
	}
}

// TestScore_NoTopologyLabel_Tolerant 无拓扑标签的节点(如 control-plane)不应让
// 调度周期失败:返回 Success + 0 分(兜底但不优先),有标签节点分数更高。
// 真实集群踩坑记录:server-0 无标签 → 原实现 Error → 全部 Pod Pending。
func TestScore_NoTopologyLabel_Tolerant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	labeled := topoNode("labeled", "numa0", 2)
	unlabeled := st.MakeNode().Name("unlabeled").Obj() // 无任何拓扑标签

	snapshot := cache.NewSnapshot(nil, []*v1.Node{labeled, unlabeled})
	fh, _ := runtime.NewFramework(ctx, nil, nil, runtime.WithSnapshotSharedLister(snapshot))
	raw, _ := NewTopologyScore(ctx, nil, fh)
	pl := raw.(*TopologyScore)
	state := sched_framework.NewCycleState()
	pod := st.MakePod().Name("g1-new").Label(topology.LabelGroupName, "g1").Label(topology.LabelGroupSize, "4").Obj()

	niUn, err := snapshot.Get("unlabeled")
	if err != nil {
		t.Fatalf("get unlabeled info: %v", err)
	}
	sUn, stUn := pl.Score(ctx, state, pod, niUn)
	if !stUn.IsSuccess() {
		t.Errorf("unlabeled node should be Success, got %v (%s)", stUn.Code(), stUn.Message())
	}
	if sUn != 0 {
		t.Errorf("unlabeled node should score 0, got %d", sUn)
	}

	niLb, _ := snapshot.Get("labeled")
	sLb, stLb := pl.Score(ctx, state, pod, niLb)
	if !stLb.IsSuccess() {
		t.Fatalf("labeled node failed: %v", stLb.Message())
	}
	if sLb <= sUn {
		t.Errorf("labeled node should outscore unlabeled: got %d vs %d", sLb, sUn)
	}
}

// 编译期断言:NewTopologyScore 满足框架插件工厂形态。
var _ runtime.PluginFactory = NewTopologyScore
var _ fwk.ScorePlugin = (*TopologyScore)(nil)
