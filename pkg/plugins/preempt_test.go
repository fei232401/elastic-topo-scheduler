package plugins

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"

	"github.com/fei/elastic-topo-scheduler/pkg/preempt"
)

// TestPostFilter_PicksLowestLossVictim 验证薄壳翻译:
// 两个节点,node0 上有个低优先级有 checkpoint 的 Pod,node1 上有高优先级无 checkpoint 的 Pod。
// 按损失最小口径,低优先级+有checkpoint 的受害者先被抢 → PostFilter 提名 node0。
func TestPostFilter_PicksLowestLossVictim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node0 := topoNode("node0", "numa0", 2)
	node1 := topoNode("node1", "numa1", 2)

	// 候选受害者:node0 低优先级+有 checkpoint,node1 高优先级+无 checkpoint
	victimLo := st.MakePod().Name("v-lo").Namespace("ns").Priority(1).Obj()
	victimLo.Spec.NodeName = "node0"

	victimHi := st.MakePod().Name("v-hi").Namespace("ns").Priority(9).Obj()
	victimHi.Spec.NodeName = "node1"

	snapshot := cache.NewSnapshot([]*v1.Pod{victimLo, victimHi}, []*v1.Node{node0, node1})
	fh, err := runtime.NewFramework(ctx, nil, nil, runtime.WithSnapshotSharedLister(snapshot))
	if err != nil {
		t.Fatalf("build framework: %v", err)
	}

	raw, err := NewTopoPreempt(ctx, nil, fh)
	if err != nil {
		t.Fatalf("build plugin: %v", err)
	}
	pl := raw.(*TopoPreempt)

	// 给 node0 的低优先级受害者打上 checkpoint 标记(模拟 annotation)
	// ckpt 字段是接口类型,测试里断言到具体实现再 Set
	ckpt := preempt.NewAnnotationCheckpoint()
	ckpt.Set(string(victimLo.UID), "true")
	pl.ckpt = ckpt

	preemptor := st.MakePod().Name("preemptor").Namespace("ns").Obj()

	res, status := pl.PostFilter(ctx, nil, preemptor, nil)
	if !status.IsSuccess() {
		t.Fatalf("PostFilter expected success, got %v (%s)", status.Code(), status.Message())
	}
	if res.NominatedNodeName != "node0" {
		t.Errorf("nominated node = %q, want %q (low-priority+checkpoint victim should be preempted first)", res.NominatedNodeName, "node0")
	}
}

// TestPostFilter_NoVictims_Unschedulable 无候选受害者时返回 Unschedulable。
func TestPostFilter_NoVictims_Unschedulable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node0 := topoNode("node0", "numa0", 2)
	snapshot := cache.NewSnapshot(nil, []*v1.Node{node0})
	fh, _ := runtime.NewFramework(ctx, nil, nil, runtime.WithSnapshotSharedLister(snapshot))
	raw, _ := NewTopoPreempt(ctx, nil, fh)
	pl := raw.(*TopoPreempt)

	preemptor := st.MakePod().Name("preemptor").Namespace("ns").Obj()
	_, status := pl.PostFilter(ctx, nil, preemptor, nil)
	if status.Code() != fwk.Unschedulable {
		t.Errorf("expected Unschedulable when no victims, got %v", status.Code())
	}
}

// 编译期断言。
var _ fwk.PostFilterPlugin = (*TopoPreempt)(nil)
