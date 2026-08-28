package plugins

// 插件注册表:把 out-of-tree 插件名映射到构造函数。
// main.go 用 RegisterOutOfTreePlugins 把整张表喂给 app.NewSchedulerCommand 的 WithPlugin。
import (
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

// OutOfTreeRegistry 返回本项目的插件注册表(name → factory)。
// 新加插件在这里登记一行即可,main.go 不用改。
func OutOfTreeRegistry() runtime.Registry {
	reg := runtime.Registry{}
	_ = reg.Register(TopologyScoreName, NewTopologyScore)
	_ = reg.Register(PreemptName, NewTopoPreempt)
	return reg
}
