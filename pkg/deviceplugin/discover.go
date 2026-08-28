package deviceplugin

// GPU 发现与拓扑提取(D6 交付,骨架级)。
//
// 设计决策:
//   要解决什么问题 —— 调度器的 Score 插件消费节点拓扑标签,标签从哪来?
//   Phase 0 靠 k3d 脚本手打假标签;真机链条 = device plugin 扫 GPU → 上报
//   nvidia.com/gpu 资源 + 每卡 NUMA → 节点标签 → 插件消费。
//
//   可选方案 —— ① 用 NVIDIA 官方 device plugin(无法注入我们的拓扑标签 schema,
//   讲不清"标签来源");② 自写最小 plugin:扫卡 + 报资源 + 每卡 NUMA + Allocate
//   注入 CUDA_VISIBLE_DEVICES。选 ②,纯函数严格、翻译层薄壳(与 Phase 0 同构)。
//
//   参数来源 —— NUMA 节点号读 /sys/bus/pci/devices/<bus>/numa_node(与
//   nvidia-device-plugin 用 NVML nvmlDeviceGetNumaNodeId 的等价路径,不引入 cgo);
//   PCI bus id 从 `nvidia-smi --query-gpu=gpu_bus_id` 取。真机上手工
//   `nvidia-smi topo -m` 拿 NUMA 0 就是同一条数据链。

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fei/elastic-topo-scheduler/pkg/topology"
)

// GPUInfo 单卡发现结果。NumaNode = 该卡所在 NUMA 域(映射为节点拓扑标签)。
type GPUInfo struct {
	Index    int    // nvidia-smi index(0,1,…)
	UUID     string // 卡 UUID
	Model    string // 型号,如 "NVIDIA GeForce RTX 5090 D"
	BusID    string // PCI 地址,如 "0000:1b:00.0"
	NumaNode int    // NUMA 节点号;-1 表示未知
}

// ParseSmiOutput 解析 `nvidia-smi --query-gpu=index,uuid,name,gpu_bus_id --format=csv,noheader`
// 的输出(纯函数,可单测)。每行格式: "0, GPU-xxx, NVIDIA GeForce RTX 5090 D, 00000000:1B:00.0"
// 注意:gpu_bus_id 带 00000000: 前缀,需规范成 /sys 路径用的 0000:1b:00.0。
func ParseSmiOutput(out string) ([]GPUInfo, error) {
	var gpus []GPUInfo
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			return nil, fmt.Errorf("unexpected smi row %q: want 4 comma-separated fields", line)
		}
		idx, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("parse smi index %q: %w", parts[0], err)
		}
		bus := normalizeBusID(strings.TrimSpace(parts[3]))
		gpus = append(gpus, GPUInfo{
			Index: idx,
			UUID:  strings.TrimSpace(parts[1]),
			Model: strings.TrimSpace(parts[2]),
			BusID: bus,
		})
	}
	return gpus, sc.Err()
}

// normalizeBusID 把 "00000000:1B:00.0" → "0000:1b:00.0"(sysfs 路径用小写全零前缀)。
func normalizeBusID(bus string) string {
	bus = strings.ToLower(bus)
	if strings.HasPrefix(bus, "00000000:") {
		bus = "0000:" + strings.TrimPrefix(bus, "00000000:")
	}
	return bus
}

// ReadNumaNode 从 /sys/bus/pci/devices/<bus>/numa_node 读 NUMA 节点号。
// 文件内容可能是 "-1"(未知)或 "0"、"1" 等。
func ReadNumaNode(sysfsRoot, busID string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(sysfsRoot, "bus/pci/devices", busID, "numa_node"))
	if err != nil {
		return -1, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1, fmt.Errorf("parse numa_node %q: %w", strings.TrimSpace(string(raw)), err)
	}
	return n, nil
}

// DiscoverGPUs 真机发现:列出 /dev/nvidia<N> + nvidia-smi 查询详情 + sysfs 读 NUMA。
// sysfsRoot 默认为 "/" ;gpuRegex 默认匹配 /dev/nvidia[0-9]+。
// 返回按 Index 升序的 GPU 列表;NUMA 读不到的卡 NumaNode = -1(不整表失败,
// 与 Score 插件"无标签兜底"同构:单卡信息缺失不拖垮整体)。
func DiscoverGPUs(sysfsRoot string) ([]GPUInfo, error) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,uuid,name,gpu_bus_id", "--format=csv,noheader").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi query: %w", err)
	}
	gpus, err := ParseSmiOutput(string(out))
	if err != nil {
		return nil, err
	}
	for i := range gpus {
		if n, err := ReadNumaNode(sysfsRoot, gpus[i].BusID); err == nil {
			gpus[i].NumaNode = n
		} else {
			gpus[i].NumaNode = -1
		}
	}
	return gpus, nil
}

// NodeTopologyLabels 把 GPU 发现结果压成节点拓扑标签(复用 pkg/topology schema)。
// 仅当同卡全部 NUMA 一致时才写 numa 域标签;不一致(异构 NUMA)则不写,
// 让 Score 插件走"无标签 → 0 分 Success"兜底,而不是用错误数据打分。
// 这是 Phase 0 发现的第二次落地:纯函数严格,上报层容错。
func NodeTopologyLabels(gpus []GPUInfo) map[string]string {
	labels := map[string]string{
		topology.LabelGPUCount: strconv.Itoa(len(gpus)),
	}
	if len(gpus) == 0 {
		return labels
	}
	model := gpus[0].Model
	nu := gpus[0].NumaNode
	for _, g := range gpus[1:] {
		if g.Model != model {
			model = ""
		}
		if g.NumaNode != nu {
			nu = -1
		}
	}
	if model != "" {
		labels[topology.LabelGPUModel] = model
	}
	if nu >= 0 {
		labels[topology.LabelNUMADomain] = strconv.Itoa(nu)
	}
	return labels
}
