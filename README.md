# elastic-topo-scheduler

> **给 kube-scheduler 写 out-of-tree 调度插件，把 GPU 分配下沉到调度器内核。**
> `Score` 插件做 GPU 拓扑感知打分（邻近度 × 碎片度），`PostFilter` 插件做抢占受害者选择（损失最小口径），
> 配合 Kueue 处理多租户排队。

`Go 1.26` · `k8s.io/kubernetes v1.35` · `Scheduling Framework` · `27 单测` · `k3s v1.35 真集群闭环`

---

## 1. 它解决什么问题

GPU 集群的调度质量不只取决于"分到几张卡"，还取决于**分到哪几张**：

- 同一块 GPU 内的 D2D（device-to-device）带宽与跨卡走 PCIe 的带宽，实测相差**约 50 倍**；
- 训练任务在多卡并行时对卡间带宽极敏感，摆错位置会让通信成为瓶颈；
- 但纯粹的"就近摆放"又会造成资源碎片，降低整体利用率。

本仓库把这一权衡实现为 kube-scheduler 的两个扩展点，并给出"代价可量化"的抢占选择。

## 2. 实测数字（真机）

| 指标 | 数值 | 含义 |
|---|---|---|
| 同卡 D2D 带宽 | **1526 GB/s** | NVLink 级 / 同域内 |
| 跨卡 PCIe 带宽 | **30.3 GB/s** | 对照基线 |
| 比值 | **≈ 50×** | 摆错位置的代价上限 |
| 拓扑感知 vs 盲目分配 | **1.32×** | L3 成本模拟（基于实测带宽常数） |

> 前两项是**真机实测**（RTX 5090 D 环境，`test/nccl_bench.py`）；
> `1.32×` 是基于实测带宽常数做的**成本模拟**，不是真机集群跑出来的端到端加速比 —— 两者证据等级不同，不并列。

## 3. 架构

```
                    kube-scheduler
                          │
              ┌───────────▼───────────┐
              │  Scheduling Framework │
              │                       │
              │  ┌─────────────────┐  │
              │  │  Score 插件      │  │   邻近度 × 碎片度 → 打分
              │  │  拓扑感知打分     │  │   权重 α 可配（含敏感性扫描）
              │  └─────────────────┘  │
              │                       │
              │  ┌─────────────────┐  │
              │  │  PostFilter 插件 │  │   抢占受害者排序
              │  │  损失最小口径     │  │   （保留 checkpoint 抽象）
              │  └─────────────────┘  │
              └───────────┬───────────┘
                          │
              ┌───────────▼───────────┐
              │  Kueue                │   多租户队列 / 准入
              └───────────┬───────────┘
                          │
              ┌───────────▼───────────┐
              │ Device Plugin 骨架     │   扫卡 + NUMA 上报 + 同域优先
              └───────────────────────┘
```

## 4. 快速开始

```bash
# 起 k3s v1.35 集群 + 编译 + 打假标签 + 串行调度验证（Phase 0 三闸）
bash config/k3d-cluster.sh

# 单元测试（27 个：21 核心 + 6 device plugin）
go test ./...

# 真机 NCCL 基准（需 GPU）
python test/nccl_bench.py

# 拓扑收益成本模拟
python test/topo_cost_sim.py
```

## 5. 目录结构

| 路径 | 内容 | 依赖 |
|---|---|---|
| `pkg/topology/` | 标签 schema + 邻近度/碎片度打分（纯函数） | 零 k8s |
| `pkg/preempt/` | 受害者排序 + checkpoint 抽象（纯函数） | 零 k8s |
| `pkg/plugins/` | Score / PostFilter 插件薄壳 + 注册 | k8s framework |
| `cmd/scheduler/` | 自定义 scheduler 入口 | k8s |
| `pkg/deviceplugin/` `cmd/device-plugin/` | GPU device plugin 骨架（扫卡 + NUMA 上报 + 同域优先） | gRPC + kubelet API |
| `test/` | 真机 NCCL 基准 / 拓扑成本模拟 / 参数敏感性扫描 | torch, python |
| `config/` | scheduler-config + k3d 闭环脚本 | — |
| `docs/` | 状态快照（真机实测证据） | — |

**设计要点**：打分与抢占排序都写成**纯函数**（`pkg/topology/`、`pkg/preempt/`），零 K8s 依赖 —— 因此可以脱离集群单测，插件层只是薄壳。

## 6. 版本钉死

- `k8s.io/kubernetes v1.35.0` + `k8s.io/* v0.35.0`（与 k3s 集群版本匹配）
- k3s `v1.35.0`（`docker.m.daocloud.io/rancher/k3s:v1.35.0-k3s1`）
- Go `1.26`（`GOTOOLCHAIN=auto`）

> 版本必须钉死：Scheduling Framework 的扩展点接口在小版本间会变动，混用会导致插件注册失败。

## 7. 诚实边界

- 拓扑收益（`1.32×`）来自**成本模拟**，不是真机集群端到端结果；模拟的输入常数（带宽）来自真机实测。
- Device Plugin 只到**骨架级**：扫卡 / NUMA 上报 / 同域优先，未做完整的资源分配与健康检查闭环。
- 抢占的 "checkpoint 抽象" 是接口级设计，未接真实训练框架的 checkpoint 实现。
