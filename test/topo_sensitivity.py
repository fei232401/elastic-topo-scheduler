#!/usr/bin/env python3
"""B2 + B4 敏感性扫描(内核层 ,)。

依赖:同目录 topo_cost_sim.py(单一常数来源,import 复用,不复制)。

核心发现(B4,比预期更有价值):
  拓扑收益 1.32× 的根本来源 = gang **原子调度**(同 gang 两 rank 同时打包到同域两卡),
  不是打分权重。非原子到达(rank 逐个来)下,即使邻近度权重 α=1,也无法恢复原子打包;
  且若邻近度只按"域"打分(把同卡也算同域),会让同 gang rank 挤一卡 → 触发 MPS 共享
  惩罚(200ms),比盲目调度(174ms)还差 = 拓扑感知负优化。

  正确建模:邻近度必须区分卡级——"同域**不同卡**"prox=1,"同卡"prox=0(容量冲突),
  让碎片度/卡级容量把 rank 拆到同域两张卡。这才是 0.7/0.3 两项目标真正配合的地方。

B2 —— MPS 惩罚稳健性:
  step_ms 里"同卡共享 SM → 计算 ×2"是线性保守模型。惩罚因子 m 扫 [1,3]:
  同卡 wall = max(T_COMPUTE·m, comm_oncard),对比同域两卡 132ms 与跨域 188.6ms。
  临界 m = 1.32(同域)/ 1.89(跨域),MPS 线性(m=2)> 两临界 → 结论稳健。
"""
import topo_cost_sim as tcs


def weighted_place(alpha: float, card_aware: bool) -> list:
    """加权打分调度器,rank 非原子到达(模拟 gang race),选最高分卡。

    card_aware=True  : 邻近度区分卡级——"同域不同卡"prox=1,"同卡"prox=0(正确建模)
    card_aware=False : 邻近度只算域——"同卡"也算同域 prox=1(错误建模,会导致挤一卡)
    score = alpha·prox + (1-alpha)·compact(compact = 1 - 单卡负载/容量,卡级)
    """
    gpus = [tcs.Gpu(d) for d in range(tcs.N_DOMAINS) for _ in range(tcs.GPUS_PER_DOMAIN)]
    order = [(0, 0), (1, 0), (2, 0), (3, 0), (0, 1), (4, 0), (1, 1), (5, 0), (2, 1), (6, 0),
             (3, 1), (7, 0), (4, 1), (5, 1), (6, 1), (7, 1)]
    gang_cards = [set() for _ in range(tcs.N_GANGS)]  # 每 gang 已占的 (domain, card)
    gangs = [[] for _ in range(tcs.N_GANGS)]
    for g, r in order:
        def score(i: int) -> float:
            d = gpus[i].domain
            c = i % tcs.GPUS_PER_DOMAIN
            same_domain = any(d == xd for (xd, _) in gang_cards[g])
            same_card = (d, c) in gang_cards[g]
            prox = 0.0
            if same_domain and (not card_aware or not same_card):
                prox = 1.0
            compact = 1.0 - gpus[i].load / tcs.CAP_PER_GPU
            return alpha * prox + (1 - alpha) * compact
        gi = min(range(len(gpus)), key=lambda i: (-score(i), gpus[i].load, i))
        gpus[gi].load += 1
        gang_cards[g].add((gpus[gi].domain, gi % tcs.GPUS_PER_DOMAIN))
        gangs[g].append((gpus[gi].domain, gi % tcs.GPUS_PER_DOMAIN))
    return gangs


def avg_wall(gangs: list) -> float:
    return sum(tcs.step_ms(s)[0] for s in gangs) / len(gangs)


def part_b4() -> None:
    print("=" * 64)
    print("B4 拓扑收益来源:原子性 vs 打分权重")
    print("=" * 64)
    topo = avg_wall(tcs.topo_place())
    blind = avg_wall(tcs.blind_place())
    print("  参考上界:原子打包(topo_place)      = %.0f ms" % topo)
    print("  参考下界:纯负载均衡(blind_place)   = %.0f ms" % blind)
    print("  → 收益 %.2f× 来自 gang **原子到达**(同 gang 两 rank 一起打包)" %
          (blind / topo))
    print()
    print("  非原子到达 + 打分,α 扫 [0,1]:")
    print("   α   | 邻近度只算域(错) | 邻近度区分卡(对)")
    for alpha in [i / 10 for i in range(11)]:
        w_bad = avg_wall(weighted_place(alpha, card_aware=False))
        w_ok = avg_wall(weighted_place(alpha, card_aware=True))
        print("  %.1f  |      %5.0f ms      |     %5.0f ms" % (alpha, w_bad, w_ok))
    print("  结论:")
    print("   邻近度只算域(错):α 越大越挤一卡,MPS 惩罚 → 200ms,负优化(比盲目 174ms 更差)")
    print("   邻近度区分卡(对):α=1 能回到 132ms,但前提是邻近度+碎片度正确配合")
    print("   收益 1.32× 的主来源 = gang 原子调度(排队框架保证),打分权重只是原子性之后的微调")


def part_b2() -> None:
    print("=" * 64)
    print("B2 MPS 惩罚稳健性:同卡共享 SM 的'计算×m',m 扫 1→3")
    print("=" * 64)
    wall_pcie = tcs.comm_ms(False, True)    # 同域两卡通信=132ms(>计算100ms → 步墙钟=通信)
    wall_cross = tcs.comm_ms(False, False)  # 跨域通信=188.6ms
    for m in [1.0, 1.3, 1.5, 1.886, 2.0, 2.5, 3.0]:
        wall_same = max(tcs.T_COMPUTE_MS * m, tcs.comm_ms(True, True))
        vs_pcie = "挤一卡更快" if wall_same < wall_pcie else "同域两卡更快 ✓"
        print("  m=%.2f → 挤一卡 %6.0f ms vs 同域 %5.0f ms → %s" % (m, wall_same, wall_pcie, vs_pcie))
    print("  临界 m(挤一卡=同域两卡)= %.2f;临界 m(挤一卡=跨域)= %.2f" %
          (wall_pcie / tcs.T_COMPUTE_MS, wall_cross / tcs.T_COMPUTE_MS))
    print("  MPS 线性(m=2)> 两临界 → '甜点=同域两卡、别挤一卡'结论稳健;" +
          "仅 m<1.32(共享 SM 零开销,物理不可能)才翻。")


if __name__ == "__main__":
    part_b4()
    print()
    part_b2()
