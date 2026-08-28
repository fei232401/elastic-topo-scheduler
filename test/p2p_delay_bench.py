#!/usr/bin/env python3
"""Phase 1 补充:双卡点对点延迟基准(NCCL send/recv ping-pong)。

补什么:all-reduce 曲线给了带宽,但"小包延迟受限"只说了现象,没分离出延迟。
  这里用最小时延 RTT(8B→256KB)→ 设计说明 = "跨卡一次握手 ~X 微秒"。

用法: torchrun --nproc-per-node=2 p2p_delay_bench.py
"""
import os
import time
import json

import torch
import torch.distributed as dist


def main -> None:
    rank = int(os.environ["RANK"])
    world = int(os.environ["WORLD_SIZE"])
    dist.init_process_group("nccl", rank=rank, world_size=world)
    torch.cuda.set_device(rank)

    for n in [2, 16, 256, 4096, 65536]:
        t = torch.ones(n, dtype=torch.float32, device="cuda")
        for _ in range(20):  # warmup
            if rank == 0:
                dist.send(t, 1)
                dist.recv(t, 1)
            else:
                dist.recv(t, 0)
                dist.send(t, 0)
        torch.cuda.synchronize
        iters = 2000
        t0 = time.time
        for _ in range(iters):
            if rank == 0:
                dist.send(t, 1)
                dist.recv(t, 1)
            else:
                dist.recv(t, 0)
                dist.send(t, 0)
        torch.cuda.synchronize
        rtt_us = (time.time - t0) / iters * 1e6
        if rank == 0:
            print(json.dumps({"size_bytes": n * 4, "rtt_us": round(rtt_us, 1),
                              "one_way_us": round(rtt_us / 2, 1)}), flush=True)

    dist.destroy_process_group


if __name__ == "__main__":
    main
