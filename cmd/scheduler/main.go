// Command scheduler 编译出自定义 kube-scheduler 二进制(out-of-tree 插件)。
//
// 关键点(k8s.io/kubernetes 特殊模块版本陷阱,见决策链 D5):
//   - 本项目 go.mod 同时 require k8s.io/kubernetes v1.36.0 与 k8s.io/kube-scheduler v0.36.0
//   - 全量 replace k8s.io/* => v0.36.0,保证与调度框架接口同代(≤1 minor skew)
//
// 启动方式:
//
//	go build -o bin/kube-scheduler ./cmd/scheduler
//	./bin/kube-scheduler --config config/scheduler-config.yaml
//
// scheduler-config.yaml 里通过 plugins 字段启用本项目的两个 out-of-tree 插件。
package main

import (
	"os"

	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"github.com/fei/elastic-topo-scheduler/pkg/plugins"
)

func main() {
	// app.NewSchedulerCommand 接收 out-of-tree 插件注册 Option;
	// 把本项目 OutOfTreeRegistry 里的每个插件名+factory 注册进调度器。
	command := app.NewSchedulerCommand(
		app.WithPlugin(plugins.TopologyScoreName, plugins.NewTopologyScore),
		app.WithPlugin(plugins.PreemptName, plugins.NewTopoPreempt),
	)
	// cli.Run 统一处理 flag 解析 / 信号 / 退出码,是官方 main 的标准收尾。
	code := cli.Run(command)
	os.Exit(code)
}
