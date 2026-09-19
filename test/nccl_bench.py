#!/usr/bin/env python3
"""内核层 Phase 1 真机 NCCL 通信基准(AutoDL 2× RTX 5090 D)。

测什么:
  1. 单卡 D2D copy 带宽 → 显存带宽量级(同卡"无通信"基线)
  2. 双卡 NCCL all-reduce 带宽曲线(1MB → 1GB)→ 跨卡 PCIe(PHB)通信量级

为什么这组数字是:
  - 同卡(显存 ~1.5TB/s)vs 跨卡(PCIe 5.0 实测 ~30-50GB/s)差 ~30-50 倍
    → 拓扑感知调度"把同组贴到同域避免跨卡拆分"的物理依据
  - all-reduce 带宽随 size 从延迟受限(小包)爬到带宽饱和(大包)
    → NCCL 通信模型,讲"为什么 GPU 调度要按通信量考虑拓扑"

用法:
  单卡基线:  python nccl_bench.py                 (WORLD_SIZE=1, 只测 D2D)
  双卡曲线:  torchrun --nproc-per-node=2 nccl_bench.py
"""
import os
import time
import json

import torch
import torch.distributed as dist


def d2d_bw(gpu: int, mb: int = 1024) -> float:
    """同卡 D2D copy 带宽(GB/s)。读+写各一次 → 通信量 2×N。"""
    torch.cuda.set_device(gpu)
    n = mb * 1024 * 1024 // 4
    src = torch.randn(n, device="cuda")
    dst = torch.empty_like(src)
    for _ in range(5):
        dst.copy_(src)
    torch.cuda.synchronize()
    iters = 50
    t0 = time.time()
    for _ in range(iters):
        dst.copy_(src)
    torch.cuda.synchronize()
    elapsed = (time.time() - t0) / iters
    return 2 * n * 4 / elapsed / 1e9


def allreduce_bw(rank: int, size_mb: int):
    """双卡 all-reduce 单次带宽。all_reduce 每 rank 发 size 收 size → 通信量 2×size。"""
    torch.cuda.set_device(rank)
    n = size_mb * 1024 * 1024 // 4
    t = torch.randn(n, device="cuda")
    for _ in range(5):
        dist.all_reduce(t)
    torch.cuda.synchronize()
    iters = 30
    t0 = time.time()
    for _ in range(iters):
        dist.all_reduce(t)
    torch.cuda.synchronize()
    elapsed = (time.time() - t0) / iters  # 每轮毫秒
    bw = 2 * n * 4 / elapsed / 1e9
    return bw, elapsed * 1e3


def main() -> None:
    rank = int(os.environ.get("RANK", "0"))
    world = int(os.environ.get("WORLD_SIZE", "1"))

    if world == 1:
        bw = d2d_bw(0)
        print(json.dumps({"phase": "d2d_single_gpu", "bw_gbps": round(bw, 1), "note": "同卡显存带宽基线"}))
        return

    dist.init_process_group("nccl", rank=rank, world_size=world)
    for mb in [1, 4, 16, 64, 256, 512, 1024]:
        bw, ms = allreduce_bw(rank, mb)
        if rank == 0:
            print(json.dumps({"phase": "allreduce_2gpu", "size_mb": mb,
                              "bw_gbps": round(bw, 1), "ms_per_iter": round(ms, 2)}), flush=True)
    if rank == 0:
        print(json.dumps({"phase": "allreduce_done", "note": "双卡 PHB/PCIe 通信曲线"}))
    dist.destroy_process_group()


if __name__ == "__main__":
    main()
