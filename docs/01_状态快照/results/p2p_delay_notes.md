Phase 1 补充测量 + 环境证据,2026-08-28(AutoDL 2× RTX 5090 D)

## 点对点延迟(NCCL send/recv ping-pong,`test/p2p_delay_bench.py`)

| 消息 | RTT | 单向 |
|---|---|---|
| 8 B | 48.7 µs | ~24 µs |
| 64 B | 54.5 µs | ~27 µs |
| 1 KB | 54.0 µs | ~27 µs |
| 16 KB | 51.0 µs | ~26 µs |
| 256 KB | 57.4 µs | ~29 µs |

→ 跨大小平稳 = **纯握手延迟 ~25 µs**(与大小无关,带宽饱和前)。
设计说明:这解释了为什么 1MB all-reduce 也才 90µs——~25µs 握手延迟主导,带宽在更大包才显形。

## 环境证据要点

- `nvidia-smi topo -m`:GPU0↔GPU1 = **PHB**(PCIe + Host Bridge),NIC0/1 = mlx5_0/1
- 驱动 595.71.05,32GB/卡,系统级 libnccl.so.2
- ⚠️ **坑(诚实标注)**:`pcie.link.gen.current=1 / width=8` 是**空闲态降频读数**(GPU 空闲时 PCIe 掉最低链路省电),不代表激活链路;实测 all-reduce 饱和 30.3 GB/s 才是真实激活上限。别拿这个读数列成"链路 gen1 x8"。
- lspci 容器内不可用(无该工具);NUMA 计数含所有 PCI 设备,不代表 GPU(2 卡确认为同 NUMA 0,见 topo)。

## 全部真机数据归档
- `nccl_bench_5090d_2026-08-28.json`(带宽曲线)
- `autodl_env_evidence.txt`(环境原始输出)
- 本文件(延迟 + 证据要点)
