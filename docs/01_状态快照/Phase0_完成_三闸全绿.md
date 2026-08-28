# 内核层 · Phase 0 状态快照(2026-08-28)

> 状态:**三闸全绿,Phase 0 完成**。✅ **Phase 1 已完成(2026-08-28,AutoDL 2×5090 D 真机)**,见 [Phase1_真机NCCL基准_完成.md](Phase1_真机NCCL基准_完成.md);本文档末尾"下一步"保留为历史计划。

## 三闸验证记录

| 闸 | 内容 | 结果 | 证据 |
|---|---|---|---|
|  | 纯函数单测全绿 | ✅ | `pkg/topology` + `pkg/preempt` + `pkg/plugins` 共 **21 测试**,0 FAIL |
|  | `go build` 出自定义 scheduler 二进制 | ✅ | `bin/kube-scheduler` 99M,v0.35.0 编译通过(插件符号 25 处) |
|  | k3d 假标签本地闭环 | ✅ | 同组 2 Pod 串行调度 → **都落 k3d-topo-demo-agent-2** |

### 第三闸闭环证据

```
pod1 → k3d-topo-demo-agent-2   (全部节点空 → 分数并列 → 任意选)
pod2 → k3d-topo-demo-agent-2   (邻近度:agent-2 已有同组 1/2=0.5;空节点=0)
打分:agent-2 = 0.7·0.5 + 0.3·1 = 0.65 > 空节点 0.7·0 + 0.3·1 = 0.3
→ agent-2 完胜。邻近度把组贴紧,碎片度只在无邻近差时定胜负。✓
```

## 本阶段的两个实战发现

### 发现 1:并发创建 → gang race(为什么 GPU 组调度需要 reservation)

Deployment 同时造 2 个同组 Pod,调度器(默认并行度 16)在**同一周期并发调度**,
各自读同一份空快照 → 两个 Pod 各落各的节点,**邻近度来不及生效**。
日志实证:两个 Pod "Attempting to schedule" 间隔仅 3ms,各 bound 到 agent-1/agent-2。

**为什么这很重要**:它天然解释了真实 GPU 组调度的痛点——朴素逐 Pod 调度无法保证
gang 整体性。这正是 Kueue(内核层)/ volcano / coscheduling 存在的原因
(PodGroup 语义 + 调度门/reservation)。这为"为什么 GPU 调度要排队框架"提供了实证。

### 发现 2:Score 插件对无标签节点返回 Error = 整个调度周期失败

真实集群必有 control-plane / 无 GPU 节点。原实现:Score 对缺 `topo.numa` 标签的
server-0 返回 `fwk.Error` → **全部 Pod 卡 Pending**(日志:`Error selecting node for pod,
running Score plugins: parse node topology: missing required label`)。

**修复(层级决策)**:`ParseNodeTopology` 纯函数保持严格(域契约);容错放插件翻译层——
节点完全无任何拓扑标签 = 非拓扑节点,返回 `Success + 0 分`(兜底但不优先);
有标签但格式非法(如 `gpu.count="abc"`)= 真实配置错误,仍 Error 大声暴露。

## 版本修正记录(设计决策 D5 已同步)

- 原误 pin `k8s.io/kubernetes v1.36.0` + `k8s.io/* v0.36.0`(最新)
- 修正为 `k8s.io/kubernetes v1.35.0` + `k8s.io/* v0.35.0`,k3d 用 daocloud `rancher/k3s:v1.35.0-k3s1`
- 理由:k3s v1.36 不存在;k3d 默认 v1.31.5 的框架接口是内嵌版(签名不同);v0.35 与 v0.36 接口签名级兼容(仅少 Placement/PodInPreBind),代码零改动
- 另一个坑:v1.36 才拆的 `k8s.io/streaming` / `cri-streaming` / `system-validators` 没有 v0.35.0 tag,replace 块按 v1.35 go.mod 收敛到 31 模块
- **scheduler kubeconfig 必须写进 config yaml 的 `clientConnection.kubeconfig`**:`--config` 提供时 `--kubeconfig` flag 被静默忽略(源码 options.go ApplyTo 只 ApplyLeaderElectionTo 不 ApplyDeprecated)

## 已交付代码

```
pkg/topology/labels.go      Label schema + ParseNodeTopology(纯函数)
pkg/topology/score.go       邻近度/碎片度加权打分(纯函数)
pkg/preempt/victim.go       受害者排序:低优先级→有checkpoint→剩余长优先(纯函数)
pkg/preempt/checkpoint.go   CheckpointState 接口 + annotation 模拟实现
pkg/plugins/topology_score.go   ScorePlugin 薄壳(含无标签容错)
pkg/plugins/preempt.go      PostFilterPlugin 薄壳
pkg/plugins/register.go     插件注册表
cmd/scheduler/main.go       自定义 scheduler 入口
config/scheduler-config.yaml  scheduler 配置(clientConnection 已修)
config/k3d-cluster.sh        三闸闭环脚本(幂等,可重复跑)
```

## 下一步(Phase 1)——✅ 已完成(2026-08-28)

> 以下 4 项为当时计划;实际执行 = AutoDL 2×5090 D 真机 NCCL 基准 + L3 成本模拟 + device plugin 骨架。
> **插件对照实验未做**(本机 2 卡同 NUMA,调度 A/B 量不出来,诚实);详见图 3 后全部内容见
> [Phase1_真机NCCL基准_完成.md](Phase1_真机NCCL基准_完成.md)。

1. ~~用户开机 AutoDL 2×4090,提供新 SSH 端口~~ → 实际 2×5090 D;实例已关机释放(SSH 端口作废)
2. ~~`nvidia-smi topo -m` 抓真拓扑 → 注入 k3d 标签~~ → 已抓(2 卡同 NUMA 0,PHB);标签来源改为 device plugin 上报链(D2 已更新)
3. ~~NCCL benchmark 量邻近 vs 拆分真实通信差 → 标定 α/β 与 fragmentation_cap~~ → 完成:同卡 1526 vs 跨卡 30.3 GB/s = **≈50×**;α/β 获方向性支撑,收益经 L3 模拟量化 = **1.32×**;`fragmentation_cap` 仍为参数位未启用
4. ~~插件对照实验~~ → 未做,原因见上;待有双 NUMA / 跨域节点时补
