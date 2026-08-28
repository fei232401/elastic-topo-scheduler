package preempt

// CheckpointState 判定 Pod 是否有 checkpoint(D4:纯函数只依赖抽象,不碰具体来源)。
// 配方复用项目二 MetricsReader:先 mock 接口,后接真实现,接口不换。
type CheckpointState interface {
	HasCheckpoint(podKey string) bool
}

// CheckpointAnnotation 约定 Phase 0 模拟用的 annotation 键。
// Phase 0 在 Pod 上打 `elastic-topo-scheduler.io/checkpoint: "true"` 表示有快照。
const CheckpointAnnotation = "elastic-topo-scheduler.io/checkpoint"

// AnnotationCheckpoint Phase 0 模拟实现:按 podKey 读 annotation 原始串,按约定解析。
// Phase 1 换成真实 checkpoint 查询实现(项目一 checkpoint 语义),接口不变。
type AnnotationCheckpoint struct {
	annotations map[string]string // podKey -> annotation 值
}

func NewAnnotationCheckpoint() *AnnotationCheckpoint {
	return &AnnotationCheckpoint{annotations: make(map[string]string)}
}

// Set 手动写入一个 podKey 的 annotation 值(Phase 0 测试/演示用;Phase 1 由扫描 Pod annotation 填充)。
func (c *AnnotationCheckpoint) Set(podKey, value string) {
	c.annotations[podKey] = value
}

// HasCheckpoint 按约定:值为 "true" 即认为有 checkpoint。
func (c *AnnotationCheckpoint) HasCheckpoint(podKey string) bool {
	return c.annotations[podKey] == "true"
}
