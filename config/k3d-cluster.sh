#!/usr/bin/env bash
# 本地 Phase 0 第三闸:k3d 假标签本地闭环。
# 验证目标:自定义 scheduler(拓扑 Score + PostFilter)在真实 k3s 集群上能把
# 同组 Pod 贴到同一拓扑域(邻近度逻辑),而不是像默认 scheduler 那样打散。
#
# 用法:bash config/k3d-cluster.sh
# 前置:k3d + docker 已装;go 可用。
# 版本:集群用 k3s v1.35.0(daocloud 镜像),与 go.mod 钉死的 v1.35 匹配(设计决策 D5 修正记录)。
set -euo pipefail

CLUSTER=topo-demo
# daocloud 镜像加速;本地已缓存或能直连 docker.io 时可改 IMAGE=rancher/k3s:v1.35.0-k3s1
IMAGE=${IMAGE:-docker.m.daocloud.io/rancher/k3s:v1.35.0-k3s1}
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== 0. 检查 k3d/docker ==="
command -v k3d >/dev/null || { echo "缺少 k3d"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker 未运行"; exit 1; }

echo "=== 1. 起集群($IMAGE)==="
if ! k3d cluster list 2>/dev/null | grep -q "^${CLUSTER} "; then
  k3d cluster create "$CLUSTER" --servers 1 --agents 3 --image "$IMAGE" -p "30080:30080@server:0"
else
  echo "(集群 ${CLUSTER} 已存在,跳过创建)"
fi
kubectl --context k3d-$CLUSTER wait --for=condition=Ready node --all --timeout=120s

echo "=== 2. 编译自定义 scheduler 二进制 ==="
cd "$REPO_ROOT"
mkdir -p bin
go build -o bin/kube-scheduler ./cmd/scheduler
ls -lh bin/kube-scheduler

echo "=== 3. 打假拓扑标签(Phase 0 模拟,Phase 1 换 nvidia-smi topo -m 真值) ==="
# agent-0/1 = numa0 域,agent-2 = numa1 域,各 2 张"卡"
for i in 0 1; do
  kubectl --context k3d-$CLUSTER label node k3d-$CLUSTER-agent-$i \
    topology.gpu-scheduler.io/topo.numa=numa0 \
    topology.gpu-scheduler.io/gpu.count=2 \
    topology.gpu-scheduler.io/gpu.model=5090D --overwrite
done
kubectl --context k3d-$CLUSTER label node k3d-$CLUSTER-agent-2 \
  topology.gpu-scheduler.io/topo.numa=numa1 \
  topology.gpu-scheduler.io/gpu.count=2 \
  topology.gpu-scheduler.io/gpu.model=4090 --overwrite

echo "=== 4. 本地起自定义 scheduler(后台,Phase 0 用本地二进制)==="
# 注意:新版 kube-scheduler 在 --config 提供时忽略 --kubeconfig flag(源码 options.go
# ApplyTo 只 ApplyLeaderElectionTo,不 ApplyDeprecated),kubeconfig 必须写进
# scheduler-config.yaml 的 clientConnection.kubeconfig。
# Phase 1(AutoDL 真 GPU 集群)再考虑 Deployment 化;本地直接跑二进制最简。
pkill -f "bin/kube-scheduler --config" 2>/dev/null || true
nohup "$REPO_ROOT/bin/kube-scheduler" \
  --config "$REPO_ROOT/config/scheduler-config.yaml" \
  --v=3 > "$REPO_ROOT/bin/scheduler.log" 2>&1 &
SCHED_PID=$!
echo "scheduler pid=$SCHED_PID,日志 bin/scheduler.log"
sleep 3
grep -q "Running configuration" "$REPO_ROOT/bin/scheduler.log" && echo "scheduler 已就绪 ✓" || echo "(scheduler 启动中,继续…)"

echo "=== 5. 闭环验证:同组 Pod 应贴到同一拓扑域 ==="
# 5.1 先清掉上次残留
kubectl --context k3d-$CLUSTER delete deployment topo-demo --ignore-not-found 2>/dev/null || true
kubectl --context k3d-$CLUSTER delete pods -l app=topo-demo --ignore-not-found 2>/dev/null || true
kubectl --context k3d-$CLUSTER wait --for=delete pods -l app=topo-demo --timeout=30s 2>/dev/null || true

# 5.2 一组 2 个 Pod,标 schedulerName=topo-scheduler + group=g1(size=2)。
# 无 GPU 请求 → Filter 全过;分数只由我们的 TopologyScore 决定。
# ⚠️ 必须串行创建(先 1 后 2):并发创建会触发 gang race——两个 Pod 同一调度
# 周期各读一份空快照,各选各的节点,邻近度来不及生效(Phase 0 实测踩坑)。
make_pod { # $1=name
  kubectl --context k3d-$CLUSTER apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $1
  labels:
    app: topo-demo
    topology.gpu-scheduler.io/group: g1
    topology.gpu-scheduler.io/group.size: "2"
spec:
  schedulerName: topo-scheduler
  containers:
    - name: nginx
      image: docker.m.daocloud.io/library/nginx:alpine
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
EOF
}

wait_scheduled { # $1=pod名,轮询直到有 nodeName
  for i in $(seq 1 30); do
    N=$(kubectl --context k3d-$CLUSTER get pod "$1" -o jsonpath='{.spec.nodeName}' 2>/dev/null)
    [ -n "$N" ] && { echo "$N"; return 0; }
    sleep 2
  done
  echo ""; return 1
}

echo "--- 5.3 先调度 Pod #1 ---"
make_pod topo-demo-1
N1=$(wait_scheduled topo-demo-1)
echo "pod1 → $N1"

echo "--- 5.4 等 pod1 绑定落快照后,再调度 Pod #2 ---"
sleep 3
make_pod topo-demo-2
N2=$(wait_scheduled topo-demo-2)
echo "pod2 → $N2"

echo "--- Pod 分布 ---"
kubectl --context k3d-$CLUSTER get pods -l app=topo-demo -o wide

echo "--- 落点:pod1=$N1 pod2=$N2(期望同节点:邻近度把组贴紧)---"
if [ -n "$N1" ] && [ "$N1" = "$N2" ]; then
  echo "✓✓ 第三闸通过:同组 2 Pod 落到同一节点($N1),邻近度逻辑在真实集群上闭环"
else
  echo "✗ 未同节点。检查 bin/scheduler.log 看 TopologyScore 打分;若 pod1 落 agent-0/1 且 pod2 贴到它,即证明邻近度生效。"
fi

echo ""
echo "=== 完成 ==="
echo "scheduler 日志:bin/scheduler.log | 停调度器:kill $SCHED_PID | 拆集群:k3d cluster delete $CLUSTER"
echo "进阶:POSTFITER 抢占演示见 config/demo-preempt.sh(可选)"
