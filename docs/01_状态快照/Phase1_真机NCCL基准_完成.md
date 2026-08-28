# Phase 1 真机 NCCL 基准 + L3 成本模拟(完成,2026-08-28)

> 前置:Phase 0 三闸全绿(`Phase0_完成_三闸全绿.md`)。本文件 = Phase 1 真机验证的完整记录,含实测数字、诚实边界。
> 代码:`test/nccl_bench.py`(真机基准)、`test/topo_cost_sim.py`(成本模拟)、`pkg/deviceplugin/` + `cmd/device-plugin/`(D6 交付)。

## 1. 真机环境(AutoDL)

| 项 | 值 |
|---|---|
| 实例 | 2× RTX 5090 D 32GB,120GB 内存 |
| 拓扑 | **2 卡同 NUMA 0**,GPU0↔GPU1 = PHB(PCIe),**无 NVLink** |
| 驱动 / CUDA | 595.71.05 / CUDA 12.8(系统),torch 2.13.0+cu130(pip,sm_120) |
| NCCL | libnccl 系统级 + nvidia-nccl-cu13 2.29.7 |
| 环境 | miniconda + py3.12 `/root/autodl-tmp/env312` |

**为什么 2 卡同 NUMA 是限制(诚实声明)**:本机任何调度器都会把 2 卡分到同一 NUMA 域,"邻近 vs 拆分"的调度对比在本机**量不出来**。因此本阶段用实测链路常数 + L3 模拟来量化拓扑收益(见 §3),而不是假装本机做了 A/B。

## 2. NCCL 通信基准(实测,`test/nccl_bench.py`)

**单卡 D2D copy(显存带宽基线)**:`1526.1 GB/s`(5090 D GDDR7 理论 1.79TB/s 的 ~85%,符合预期)

**双卡 all-reduce 带宽曲线(每轮 30 次取均值,`torchrun --nproc-per-node=2`)**:

| tensor | 带宽 | 每轮耗时 |
|---|---|---|
| 1 MB | 23.4 GB/s | 0.09 ms |
| 4 MB | 27.7 GB/s | 0.30 ms |
| 16 MB | 28.7 GB/s | 1.17 ms |
| 64 MB | 29.0 GB/s | 4.62 ms |
| 256 MB | 30.2 GB/s | 17.8 ms |
| 512 MB | 30.2 GB/s | 35.5 ms |
| 1024 MB | 30.3 GB/s | 71.0 ms |

**核心数字:同卡 1526 GB/s vs 跨卡 PCIe 30.3 GB/s = ≈50 倍。**
小包即到 23GB/s(延迟受限);大包饱和 30.3GB/s(PHB/PCIe 5.0 上限)。**A 级实测证据,无掺水。**

**补充:点对点延迟(ping-pong)** — 8B RTT 48.7µs(单向 ~25µs),跨大小平稳 = 纯握手延迟 ~25µs。这解释了为何 1MB all-reduce 才 90µs(握手主导)。原始数据 + 环境证据见 `results/`(含一个坑:`pcie.link.gen.current=1` 是空闲降频读数,不代表激活链路)。

## 3. L3 拓扑成本模拟(`test/topo_cost_sim.py`,实测常数 → 量化收益)

**输入参数来源**:

| 常数 | 值 | 来源 |
|---|---|---|
| BW_ONCARD | 1526 GB/s | 本机单卡 D2D 实测 |
| BW_PCIE | 30.3 GB/s | 本机双卡 all-reduce 1GB 饱和实测 |
| 延迟 | 0.09 ms | 本机 1MB all-reduce 实测 |
| BW_CROSS(跨域) | 21.2 GB/s | **假设** = PCIe × 0.7(本机同 NUMA,无法实测,明确标注) |
| 每步 grad | 4.0 GB | 合成 workload(≈2B 模型 fp32 grad) |
| 每步计算 | 100 ms | 合成 workload |

**模型**:步墙钟 = max(计算, 通信);R=2 ring all-reduce 每 rank 通信量 = S;同卡共享 SM → 计算 ×2(MPS 线性保守模型)。

**单次 4GB all-reduce 链路对比**:
```
同卡(显存)   2.6 ms      ← 实测常数
同域跨卡(PCIe) 132.0 ms   ← 实测常数
跨域(假设)    188.6 ms   ← PCIe×0.7 假设
```

**集群级(4 域×2 卡,8 个 TP-2 gang,每卡容量 2)**:
```
盲目调度(rank 非原子到达,最小负载卡):gang 分布 同域×2 + 跨域×6 | 邻近度 0.62 | 步墙钟 174 ms
拓扑感知(原子同域打包):              gang 分布 同域×8          | 邻近度 1.00 | 步墙钟 132 ms
```
**拓扑感知 vs 盲目 = 1.32× 每步提速。**

### 关键诚实结论
1. **链路差 50 倍是物理事实**,但调度决策看的是**步墙钟 = max(计算,通信)**,链路差不直接等于收益差。
2. 本参数下**甜点是"同域两卡"(135ms),不是"挤进一张卡"(200ms)**——同卡省通信但 MPS 共享亏 2× 计算。若通信远超计算(超大 grad / 极快计算)同卡才赢。
3. 盲目调度的失败机制 = **gang 非原子到达 → 跨域拆分**(对应 Phase 0 的 gang race 发现):rank 逐个落最小负载卡,容量压力下同 gang 被拆到不同域,邻近度掉到 0.62。
4. 盲目最小负载的一个有趣副作用:若 rank 恰好同落一卡,会变成 MPS 同卡共置 → 通信快但计算亏。**默认调度不感知这些,正是拓扑插件存在的理由。**

## 4. k3s 真 GPU 调度闭环:容器内不可行(诚实放弃)

AutoDL 容器安全边界实测:
- `unshare -n` → **Operation not permitted**(无法建 netns,k3s flannel/CNI 必需)
- 无 iptables、无 docker/containerd、CapEff `0xa80425fb` 无 cap_sys_admin/cap_net_admin

结论:容器内跑不了 k3s。**计划预案"尽力、不可行则诚实放弃"**。调度器闭环由 Phase 0 本地 k3d 承担(假标签),真机价值转移到 §2 实测常数 + §3 模拟 + §5 device plugin。

## 5. Device Plugin 拓扑上报(D6 交付,代码级)

`cmd/device-plugin/` + `pkg/deviceplugin/`:最小 GPU device plugin 骨架,6 个新单测。
- **发现**:`nvidia-smi --query-gpu=index,uuid,name,gpu_bus_id` + 每卡 NUMA 读 `/sys/bus/pci/devices/<bus>/numa_node`(与官方 NVML 路径等价,不引 cgo)
- **上报**:`ListAndWatch` 把 `Device{ID, Topology.Nodes[NUMANode]}` 流给 kubelet = 节点拓扑标签数据来源
- **同域优先**:`GetPreferredAllocation` 实现"与 MustInclude 同 NUMA 域优先"= 调度器邻近度在设备层的对应物
- **分配**:`Allocate` 注入 `CUDA_VISIBLE_DEVICES`
- 节点标签 `NodeTopologyLabels` 复用 `pkg/topology` schema;异构 NUMA 不写 numa 标签(Score 走"无标签→0 分"兜底)——Phase 0 容错发现的第二次落地

## 6. 测试与代码清单

```
go build ./...  # BUILD OK(含 device-plugin 二进制)
go test ./...   # 27 全绿 = 21 原有 + 6 新(deviceplugin)
```

新增:
| 文件 | 内容 |
|---|---|
| `test/nccl_bench.py` | 真机 NCCL 基准(单卡 D2D + 双卡 all-reduce 曲线) |
| `test/topo_cost_sim.py` | L3 成本模拟(实测常数) |
| `pkg/deviceplugin/discover.go` | GPU 发现 + 拓扑标签(纯函数) |
| `pkg/deviceplugin/server.go` | DevicePluginServer 薄壳 |
| `pkg/deviceplugin/discover_test.go` | 6 单测 |
| `cmd/device-plugin/main.go` | 入口(注册 kubelet + serve) |
| `docs/01_状态快照/results/` | 基准原始输出归档 |

## 7. 小结

> "我在 AutoDL 真机(2×5090 D)实测了 GPU 通信:同卡显存带宽 1.53TB/s,跨卡 PCIe 只有 30GB/s,**差 50 倍**——这就是拓扑感知调度'别把同组跨域拆分'的物理依据。因为本机 2 卡同 NUMA,调度 A/B 量不出来,我用实测常数做了成本模拟:盲目调度(gang 非原子到达)把 gang 拆跨域,邻近度掉到 0.62、步墙钟 174ms;拓扑感知保持同域,132ms,提速 1.32×。关键是我发现甜点是'同域两卡'不是'挤一卡'(MPS 共享亏 2× 计算),链路差 50 倍不等于收益 50 倍。数据面这块我还交付了最小 device plugin:扫描 GPU、上报每卡 NUMA、GetPreferredAllocation 同域优先。"

## 8. 待办 / stretch
- [ ] 跨 NUMA 域带宽实测(需 2 NUMA 节点真机,替代 BW_CROSS 假设)→ 跨域数字来自假设,已标注,待真机标定
- [ ] 热插拔支持(当前骨架 ListAndWatch 一次全量后阻塞)
- [ ] 模拟扩展:TP-4 / 多租户混排 / gang 分批到达扫描
