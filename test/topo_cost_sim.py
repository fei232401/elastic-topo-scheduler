#!/usr/bin/env python3
"""内核层 Phase 1 L3 拓扑成本模拟。

要解决的问题:
  2 卡同 NUMA 真机量不出"邻近 vs 拆分"的调度收益(任何调度结果一样),
  但内核层的核心主张 = 拓扑感知调度避免跨域拆分。用真机实测带宽常数,
  在合成多域集群上量化这个收益。

方案选择:
  - 不用纯纸面理论带宽(无实测锚点,不诚实)
  - 不做真 k8s 集群(容器内 netns/iptables 不可用,k3s 不可行,已放弃)
  - 用 实测常数 + 透明成本模型(本次所选):锚点保真、可逐行审查、本地可跑

参数来源:
  BW_ONCARD = 1526 GB/s  ← 单卡 D2D copy 实测(5090 D,cc 12.0)
  BW_PCIE   = 30.3 GB/s  ← 双卡 all-reduce 1GB 饱和带宽实测(PHB/PCIe)
  LATENCY   = 0.09 ms    ← 1MB all-reduce 每轮延迟实测
  BW_CROSS  = 21.2 GB/s  ← 跨 NUMA 假设 = PCIe × 0.7(本机同 NUMA,无法实测)
  T_COMPUTE = 100 ms/步   ← 合成 workload 参数(小型 LLM 一步,非物理常数)
  SIZE_GB   = 4.0        ← 每步 grad all-reduce 大小(约 2B 模型 fp32 grad 量级)

模型(每步 = max(计算, 通信),通信可部分与计算重叠;通信超过计算则显形):
  - R=2 ring all-reduce:每 rank 通信量 = S 字节,耗时 ≈ S / 链路带宽 + 延迟下限
  - 同卡共享 SM:MPS 式共置,r rank 同卡 → 每 rank 计算 ≈ T_COMPUTE × r(线性,保守模型)
  - 链路:同卡=显存 1526 | 同域跨卡=PCIe 30.3 | 跨域=21.2(假设)

关键诚实结论(代码跑出来的):
  1. 通信链路同卡↔跨卡差 50 倍(实测),但调度决策看步墙钟
  2. 本参数下甜点 = 同域两卡(132ms),不是同卡(200ms):同卡省通信但亏 2× 计算
  3. 盲目调度(gang 非原子到达 → 跨域拆分)平均步 174ms vs 拓扑感知 132ms = 1.32×
"""
from dataclasses import dataclass

# ---- 实测常数(AutoDL 2× RTX 5090 D,2026-08-28) ----
BW_ONCARD = 1526.0        # GB/s,单卡 D2D copy
BW_PCIE = 30.3            # GB/s,双卡 all-reduce 1GB 饱和
LATENCY_MS = 0.09         # ms,1MB all-reduce 每轮
BW_CROSS = BW_PCIE * 0.7  # 跨 NUMA 假设(标注:非实测)
SIZE_GB = 4.0             # 每步 grad all-reduce(合成 workload 参数)
T_COMPUTE_MS = 100.0      # 每步计算,单 rank 独占卡(合成 workload 参数)

N_DOMAINS = 4             # 合成集群:4 域 × 2 卡 = 8 卡(仿照真机"1 域 2 卡"×4)
GPUS_PER_DOMAIN = 2
CAP_PER_GPU = 2           # 每卡最多 rank 数(MPS/时间片)

N_GANGS = 8               # TP-2 gang 数
RANKS_PER_GANG = 2


# ---------- 单次 all-reduce 链路对比(全实测,无假设) ----------
def comm_ms(same_card: bool, same_domain: bool) -> float:
    bw = BW_ONCARD if same_card else (BW_PCIE if same_domain else BW_CROSS)
    return max(SIZE_GB / bw * 1000.0, LATENCY_MS)


def single_reduce_ladder -> None:
    print("【0】单次 %.1fGB all-reduce 链路对比(通信耗时)" % SIZE_GB)
    for label, sc, sd in [("同卡(显存)", True, True), ("同域跨卡(PCIe)", False, True), ("跨域(假设)", False, False)]:
        print("   %-16s %7.1f ms" % (label, comm_ms(sc, sd)))
    print("   → 链路同卡↔跨卡差 %.0f× (实测 1526 vs 30.3 GB/s);跨域为 PCIe×0.7 假设" %
          (comm_ms(False, True) / comm_ms(True, True)))


# ---------- 集群级:两种调度 ----------
@dataclass
class Gpu:
    domain: int
    load: int = 0


def step_ms(slots: list) -> tuple[float, str, float]:
    """一个 gang 的每步墙钟 = max(计算, 通信)。返回 (墙钟, 链路, 邻近度)。
    同卡:计算 ×2(MPS 共享),通信走显存;同域两卡:独占,PCIe;跨域:独占,跨域链路。"""
    d0, d1 = slots[0][0], slots[1][0]
    c0, c1 = slots[0][1], slots[1][1]
    if d0 == d1 and c0 == c1:
        compute = T_COMPUTE_MS * 2  # 同卡共享 SM(MPS,线性保守模型)
        ms, link = comm_ms(True, True), "同卡(共享)"
    elif d0 == d1:
        compute, ms, link = T_COMPUTE_MS, comm_ms(False, True), "同域PCIe"
    else:
        compute, ms, link = T_COMPUTE_MS, comm_ms(False, False), "跨域(假设)"
    return max(compute, ms), link, (1.0 if d0 == d1 else 0.5)


def blind_place -> list:
    """盲目调度:rank 逐个到达(同 gang 两 rank 之间隔≥1 个其它 gang 的 rank,模拟
    独立调度周期/gang race),每个 rank 落到负载最低的卡(并列取小下标)。
    机制:gang 非原子到达 + 容量压力 → 同 gang 的 rank 落到不同域。"""
    gpus = [Gpu(d) for d in range(N_DOMAINS) for _ in range(GPUS_PER_DOMAIN)]
    # 到达顺序:g0r0,g1r0,g2r0,g3r0,g0r1,g4r0,g1r1,g5r0,g2r1,g6r0,g3r1,g7r0,g4r1,g5r1,g6r1,g7r1
    order = [(0, 0), (1, 0), (2, 0), (3, 0), (0, 1), (4, 0), (1, 1), (5, 0), (2, 1), (6, 0),
             (3, 1), (7, 0), (4, 1), (5, 1), (6, 1), (7, 1)]
    gangs = [[] for _ in range(N_GANGS)]
    for g, r in order:
        gi = min(range(len(gpus)), key=lambda i: (gpus[i].load, i))
        gpus[gi].load += 1
        gangs[g].append((gpus[gi].domain, gi % GPUS_PER_DOMAIN))
    return gangs


def topo_place -> list:
    """拓扑感知调度:每个 gang 原子放进同一域(邻近度优先,模拟 0.7·邻近+0.3·碎片),
    先占满域内两卡。同 gang 必同域 → 邻近度 1.0。"""
    domains = [0, 0, 0, 0]  # 每域已用卡数
    gangs = []
    for _ in range(N_GANGS):
        d = next(i for i, used in enumerate(domains) if used + 1 <= GPUS_PER_DOMAIN)
        gpu0, gpu1 = d * 2, d * 2 + 1
        gangs.append([(d, 0), (d, 1)])
        domains[d] += 1
    return gangs


def cluster_compare -> None:
    print("\n【1】集群级(%d 域×2 卡,TP-2 gang ×%d,每卡容量 %d)" % (N_DOMAINS, N_GANGS, CAP_PER_GPU))
    for label, fn in [("盲目(rank 非原子到达,最小负载卡)", blind_place), ("拓扑感知(原子同域打包)", topo_place)]:
        gangs = fn
        by_link = {}
        walls, prox = [], []
        for slots in gangs:
            wall, link, p = step_ms(slots)
            by_link[link] = by_link.get(link, 0) + 1
            walls.append(wall)
            prox.append(p)
        print("  %s:" % label)
        print("    gang 分布:%s" % ", ".join(f"{k}×{v}" for k, v in sorted(by_link.items)))
        print("    平均邻近度 %.2f | 平均步墙钟 %.0f ms" % (sum(prox) / len(prox), sum(walls) / len(walls)))


# ---------- 主流程 ----------
def main -> None:
    print("拓扑成本模拟(实测常数):单卡 %.0f | 同域 PCIe %.1f | 跨域 %.1f GB/s(假设)" % (BW_ONCARD, BW_PCIE, BW_CROSS))
    print("workload 参数:grad %.1fGB/步 | 计算 %.0f ms/步 | TP-2" % (SIZE_GB, T_COMPUTE_MS))
    print("-" * 64)
    single_reduce_ladder
    cluster_compare


if __name__ == "__main__":
    main
