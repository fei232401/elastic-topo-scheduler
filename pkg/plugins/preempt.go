package plugins

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fwk "k8s.io/kube-scheduler/framework"

	"github.com/fei/elastic-topo-scheduler/pkg/preempt"
)

// PreemptName 插件名(与 scheduler-config.yaml / main.go 注册处一致)。
const PreemptName = "TopoPreempt"

// TopoPreempt 抢占受害者选择插件:实现 PostFilterPlugin。
// 薄壳:PostFilter 阶段触发 → 收集候选受害者 → 纯函数 SortVictims(损失最小口径)→ 返回提名节点。
type TopoPreempt struct {
	handle fwk.Handle
	// ckpt 提供 checkpoint 查询。Phase 0 用 annotation 模拟,Phase 1 换真实现。
	ckpt preempt.CheckpointState
}

// 编译期断言:必须实现 PostFilterPlugin。
var _ fwk.PostFilterPlugin = (*TopoPreempt)(nil)

// NewTopoPreempt runtime.PluginFactory 形态构造函数。
func NewTopoPreempt(_ context.Context, _ runtime.Object, handle fwk.Handle) (fwk.Plugin, error) {
	return &TopoPreempt{handle: handle, ckpt: preempt.NewAnnotationCheckpoint()}, nil
}

// Name 返回插件名。
func (pl *TopoPreempt) Name() string { return PreemptName }

// PostFilter 在调度周期因 Unschedulable 失败后触发,尝试找受害者腾地。
//
// 完整抢占流程是"选节点 → 驱逐受害者 → 重新调度"三步(见决策链 A4),
// Phase 0 骨架只做**纯决策部分**:把所有可抢 Pod 按损失最小排序,提名最优节点的第一名。
// 真实驱逐/重调由框架与 kubelet 完成,插件只返回提名(PostFilterResult.NominatedNode)。
//
// 返回值:
//   - Success + 提名节点 → 框架把 preemptor 的 nominatedNodeName 写回,触发新一轮调度
//   - Unschedulable → 无可抢目标,Pod 继续排队
func (pl *TopoPreempt) PostFilter(ctx context.Context, state fwk.CycleState, pod *v1.Pod, filteredNodeStatusMap fwk.NodeToStatusReader) (*fwk.PostFilterResult, *fwk.Status) {
	// 收集候选:遍历所有节点,把有 Pod 可抢的节点纳入。
	// Phase 0 简化:只挑"该节点上有非自身、且能满足受害者维度"的 Pod。
	var best *preempt.VictimCandidate
	bestNode := ""

	nodes, err := pl.handle.SnapshotSharedLister().NodeInfos().List()
	if err != nil {
		return nil, fwk.NewStatus(fwk.Error, fmt.Sprintf("list nodes: %v", err))
	}
	for _, node := range nodes {
		nodeName := node.Node().GetName()
		candidates := pl.collectVictims(node, pod)
		if len(candidates) == 0 {
			continue
		}
		// 纯函数:按损失最小排序,第一名 = 该节点最优受害者
		top := preempt.SortVictims(candidates)[0]
		if best == nil || preempt.LessVictim(top, *best) {
			best = &top
			bestNode = nodeName
		}
	}

	if best == nil {
		return nil, fwk.NewStatus(fwk.Unschedulable, "no preemptable victim found")
	}
	fmt.Printf("[TopoPreempt] preempt pod %s on node %s (victim %s)\n", pod.Name, bestNode, best.PodKey)
	// PostFilterResult 包装 NominatingInfo,框架据此写回 preemptor 的 nominatedNodeName
	return &fwk.PostFilterResult{NominatingInfo: &fwk.NominatingInfo{NominatedNodeName: bestNode}}, fwk.NewStatus(fwk.Success)
}

// collectVictims 收集该节点上可抢的 Pod 候选。
// Phase 0 简化:排除 preemptor 自身,其余 Pod 全按"已知 checkpoint 状态 + 假定剩余时长 0"入列。
// 剩余运行时间 Phase 0 无真实训练信号,全部按 0 处理(见决策链 D4:先模拟,Phase 1 接真数据)。
func (pl *TopoPreempt) collectVictims(node fwk.NodeInfo, preemptor *v1.Pod) []preempt.VictimCandidate {
	var out []preempt.VictimCandidate
	for _, pi := range node.GetPods() {
		p := pi.GetPod()
		// 排除 preemptor 自身。UID 未设置(如测试构造)时跳过比较,避免空 UID 误判全部排除。
		if p == nil || (preemptor.UID != "" && p.UID == preemptor.UID) {
			continue
		}
		prio := int32(0) // 未设优先级类 = 最低,先被抢
		if p.Spec.Priority != nil {
			prio = *p.Spec.Priority
		}
		out = append(out, preempt.VictimCandidate{
			PodKey:           string(p.UID),
			Priority:         prio,
			HasCheckpoint:    pl.ckpt.HasCheckpoint(string(p.UID)),
			RemainingRuntime: 0, // Phase 0 无信号,统一 0;Phase 1 接训练进度估计
		})
	}
	return out
}

