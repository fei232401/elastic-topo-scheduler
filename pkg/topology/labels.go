// Package topology 提供拓扑感知打分的纯函数(零 k8s 依赖,便于单测与复用)。
package topology

import (
	"fmt"
	"strconv"
)

// 标签 schema(D2 定稿):用 Label 不用 Annotation,前缀 topology.gpu-scheduler.io/,
// Phase 1 每节点 = 一个拓扑域(即一组卡共享一个 NUMA 域)。
const (
	LabelPrefix     = "topology.gpu-scheduler.io/"
	LabelNUMADomain = LabelPrefix + "topo.numa" // 节点所属 NUMA 域 ID(Phase 1 值来自 nvidia-smi topo -m)
	LabelGPUCount   = LabelPrefix + "gpu.count" // 卡数
	LabelGPUModel   = LabelPrefix + "gpu.model" // GPU 型号(如 5090 D / A100)

	// Pod 侧标签(请求组声明):Pod 打上这两个标签 = 声明它属于"要去同一拓扑域"的组
	LabelGroupName = LabelPrefix + "group"      // 组名,同组 Pod 共享
	LabelGroupSize = LabelPrefix + "group.size" // 组期望规模(总 Pod 数)
)

// NodeTopology 是插件从节点标签解析出的纯数据快照,不含任何 k8s 对象。
type NodeTopology struct {
	NUMADomain string
	GPUCount   int
	GPUModel   string
}

// ParseNodeTopology 从 label 映射解析节点拓扑。
// topo.numa 与 gpu.count 必填,缺失或非法返回 error(Phase 1 约定所有候选节点都带这两标签);
// gpu.model 可选,缺省 "unknown"。
func ParseNodeTopology(labels map[string]string) (NodeTopology, error) {
	numa, ok := labels[LabelNUMADomain]
	if !ok || numa == "" {
		return NodeTopology{}, fmt.Errorf("missing required label %s", LabelNUMADomain)
	}
	countStr, ok := labels[LabelGPUCount]
	if !ok || countStr == "" {
		return NodeTopology{}, fmt.Errorf("missing required label %s", LabelGPUCount)
	}
	count, err := strconv.Atoi(countStr)
	if err != nil {
		return NodeTopology{}, fmt.Errorf("invalid %s %q: %w", LabelGPUCount, countStr, err)
	}
	model := labels[LabelGPUModel]
	if model == "" {
		model = "unknown"
	}
	return NodeTopology{NUMADomain: numa, GPUCount: count, GPUModel: model}, nil
}
