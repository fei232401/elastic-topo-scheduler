package topology

// 打分模型(D1 定稿):总分 = 0.7·邻近度 + 0.3·碎片度,均∈[0,1]。
// 参数来源诚实说明:α/β 为初始经验值(拍板),Phase 1 用 NCCL benchmark 实测通信差标定。
const (
	WeightProximity   = 0.7
	WeightCompactness = 0.3
)

// Group 描述一个请求组:共享同一拓扑目标的一组 Pod(Phase 1 语义 = 要去同一 NUMA 域的 Pod 群)。
type Group struct {
	Name string
	Size int // 组期望规模(总 Pod 数)
}

// NodeScoreInput 是单节点打分的纯输入:节点现状快照 + 组定义 + 组内已驻留数 + 已占用卡数。
// 有状态性(见决策链 D1):邻近度依赖"同组已驻留数",这是跨 Pod 的状态,不是每 Pod 独立纯函数。
type NodeScoreInput struct {
	Node            NodeTopology
	Group           Group
	GroupPodsOnNode int // 已调度到该节点的同组 Pod 数
	OccupiedGPUs    int // 该节点已占用卡数(所有组合计)
}

// ScoringParams 打分参数。FragmentationCap = 碎片硬门(预留,Phase 0 不启用):
// 剩余空间比例低于该阈值 → 节点不可用(Filter 语义);<=0 表示关闭。
type ScoringParams struct {
	FragmentationCap float64
}

// Proximity 邻近度 = min(1, 同组已驻留数 / 组规模)。
// 同组 Pod 在这个节点已驻留越多,通信局部性越好 → 越"近"。
// 防御:组规模 <= 0 视为无约束,返回 1。
func Proximity(in NodeScoreInput) float64 {
	if in.Group.Size <= 0 {
		return 1
	}
	p := float64(in.GroupPodsOnNode) / float64(in.Group.Size)
	if p > 1 {
		return 1
	}
	return p
}

// Compactness 碎片度(紧凑度) = 1 - 占用率 = 剩余空闲比例。
// 剩余空间越整,新组越容易整块放下不被拆散 → 越适合。
// 防御:卡数为 0 视为空节点,返回 1。
func Compactness(in NodeScoreInput) float64 {
	if in.Node.GPUCount <= 0 {
		return 1
	}
	occ := float64(in.OccupiedGPUs) / float64(in.Node.GPUCount)
	if occ > 1 {
		occ = 1
	}
	return 1 - occ
}

// TotalScore 加权总分 ∈[0,1]。
// 第二返回值 feasible=false 表示节点被碎片硬门拒绝(不参与优选)。
func TotalScore(in NodeScoreInput, p ScoringParams) (float64, bool) {
	if p.FragmentationCap > 0 {
		if in.Node.GPUCount <= 0 {
			return 0, false
		}
		free := 1 - float64(in.OccupiedGPUs)/float64(in.Node.GPUCount)
		if free < p.FragmentationCap {
			return 0, false
		}
	}
	return WeightProximity*Proximity(in) + WeightCompactness*Compactness(in), true
}
