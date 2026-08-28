// Package plugins 是 kube-scheduler out-of-tree 插件薄壳。
// 决策逻辑全部在 pkg/topology 纯函数里(已单测),这里只做"框架数据 → 纯函数 → 框架返回值"翻译。
package plugins

import (
	"context"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fwk "k8s.io/kube-scheduler/framework"

	"github.com/fei/elastic-topo-scheduler/pkg/topology"
)

// TopologyScoreName 插件名(在 scheduler-config.yaml 与 main.go 注册处保持一致)。
const TopologyScoreName = "TopologyScore"

// gpuResourceName Phase 1 读的扩展资源名(k8s 标准 GPU 扩展资源)。
const gpuResourceName = v1.ResourceName("nvidia.com/gpu")

// TopologyScore 拓扑感知打分插件:实现 ScorePlugin。
// 薄壳:每个节点在 Score 阶段调用 → 解析节点标签 → 算邻近度/碎片度加权总分。
type TopologyScore struct {
	handle fwk.Handle
}

// 编译期断言:必须实现 ScorePlugin(接口变了立刻编译报错,不靠运行时)。
var _ fwk.ScorePlugin = (*TopologyScore)(nil)

// NewTopologyScore 是 runtime.PluginFactory 形态的构造函数:
// signature = func(ctx, configuration runtime.Object, handle fwk.Handle) (fwk.Plugin, error)。
func NewTopologyScore(_ context.Context, _ runtime.Object, handle fwk.Handle) (fwk.Plugin, error) {
	return &TopologyScore{handle: handle}, nil
}

// Name 返回插件名,调度框架按名字查找注册表。
func (pl *TopologyScore) Name() string { return TopologyScoreName }

// Score 对每个通过 Filter 的候选节点打分,返回框架约定整数区间 [0, 100]。
// 翻译流程:
//  1. nodeInfo.Node().Labels → 纯函数 ParseNodeTopology 解析拓扑标签
//  2. Pod 标签 → 请求组定义(去同一拓扑域的一组 Pod)
//  3. 数该节点同组 Pod 数 + 已请求 GPU 数
//  4. 纯函数 TotalScore → [0,1] 总分 → ×100 转框架整数
func (pl *TopologyScore) Score(ctx context.Context, state fwk.CycleState, pod *v1.Pod, nodeInfo fwk.NodeInfo) (int64, *fwk.Status) {
	node := nodeInfo.Node()
	if node == nil {
		return 0, fwk.NewStatus(fwk.Error, "nodeInfo.Node() is nil")
	}

	// 容错策略(翻译层):节点完全没有任何拓扑标签 = 非拓扑节点
	// (control-plane / 无 GPU 节点),不给 0 分 Success,可作兜底但绝不优先。
	// 注意不能直接 Parse 出错就 Return 0——Score 阶段返回 Error 会让整个
	// 调度周期失败(真实集群已踩坑:server-0 无标签 → 全部 Pod Pending)。
	if !nodeHasTopologyLabel(node.Labels) {
		return 0, fwk.NewStatus(fwk.Success)
	}

	nt, err := topology.ParseNodeTopology(node.Labels)
	if err != nil {
		// 有部分拓扑标签但格式非法(如 gpu.count="abc"):真实配置错误,大声暴露
		return 0, fwk.NewStatus(fwk.Error, fmt.Sprintf("parse node topology: %v", err))
	}

	group, gerr := groupFromPod(pod)
	if gerr != nil {
		return 0, fwk.NewStatus(fwk.Error, gerr.Error())
	}

	in := topology.NodeScoreInput{
		Node:            nt,
		Group:           group,
		GroupPodsOnNode: countGroupPods(nodeInfo, group.Name),
		OccupiedGPUs:    countRequestedGPUs(nodeInfo),
	}
	score, feasible := topology.TotalScore(in, topology.ScoringParams{})
	if !feasible {
		// 碎片硬门拒绝:Phase 0 cap 关闭,此分支默认不触发
		return 0, fwk.NewStatus(fwk.Success)
	}
	return int64(score * 100), fwk.NewStatus(fwk.Success)
}

// ScoreExtensions 本项目不需要多插件归一化,返回 nil(加权在纯函数内做,D1)。
func (pl *TopologyScore) ScoreExtensions() fwk.ScoreExtensions { return nil }

// nodeHasTopologyLabel 判断节点是否携带任一拓扑标签。
// 无 = 非拓扑节点(容错,给 0 分);有 = 进入严格 Parse(格式错则 Error)。
// 只判"有没有",格式校验交给 ParseNodeTopology 纯函数。
func nodeHasTopologyLabel(labels map[string]string) bool {
	for _, k := range []string{topology.LabelNUMADomain, topology.LabelGPUCount, topology.LabelGPUModel} {
		if _, ok := labels[k]; ok {
			return true
		}
	}
	return false
}

// --- 辅助:框架数据 → 纯函数输入 ---

// groupFromPod 从 Pod 标签提取请求组。Phase 1 约定:
// Pod 带 topology.gpu-scheduler.io/group=<name> 与 /group.size=<N>,
// 表示它属于一个要去同一拓扑域的组,组期望规模 N。
// 缺省:单 Pod 自成一组的退化情形(Size=1)。
func groupFromPod(pod *v1.Pod) (topology.Group, error) {
	name := pod.Labels[topology.LabelGroupName]
	if name == "" {
		// 无组标签 → 单 Pod 自成一组的退化情形。
		// 用 UID 唯一标识;UID 未设置(测试/构造)则退回 namespace/name。
		name = string(pod.UID)
		if name == "" {
			name = pod.Namespace + "/" + pod.Name
		}
	}
	sizeStr := pod.Labels[topology.LabelGroupSize]
	if sizeStr == "" {
		sizeStr = "1"
	}
	size, err := strconv.Atoi(sizeStr)
	if err != nil || size <= 0 {
		return topology.Group{}, fmt.Errorf("invalid group.size %q on pod %s", sizeStr, pod.Name)
	}
	return topology.Group{Name: name, Size: size}, nil
}

// countGroupPods 数该节点上已调度、属于同组 Name 的 Pod 数(邻近度输入)。
func countGroupPods(nodeInfo fwk.NodeInfo, groupName string) int {
	n := 0
	for _, pi := range nodeInfo.GetPods() {
		if p := pi.GetPod(); p != nil && p.Labels[topology.LabelGroupName] == groupName {
			n++
		}
	}
	return n
}

// countRequestedGPUs 统计该节点上所有 Pod 已请求的 GPU 总数(碎片度输入)。
// Phase 1:读扩展资源 nvidia.com/gpu 的请求量(设备分配量由 device plugin 汇报,这里用请求量近似占用)。
func countRequestedGPUs(nodeInfo fwk.NodeInfo) int {
	req := nodeInfo.GetRequested()
	if req == nil {
		return 0
	}
	return int(req.GetScalarResources()[gpuResourceName])
}
