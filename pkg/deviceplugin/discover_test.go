package deviceplugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fei/elastic-topo-scheduler/pkg/topology"
)

func TestParseSmiOutput(t *testing.T) {
	// 真机格式(2026-08-28,5090 D):index, uuid, name, gpu_bus_id
	out := "0, GPU-aaaaaaaaaaaaaaaa, NVIDIA GeForce RTX 5090 D, 00000000:1B:00.0\n" +
		"1, GPU-bbbbbbbbbbbbbbbb, NVIDIA GeForce RTX 5090 D, 00000000:4B:00.0\n"
	gpus, err := ParseSmiOutput(out)
	if err != nil {
		t.Fatalf("ParseSmiOutput: %v", err)
	}
	if len(gpus) != 2 {
		t.Fatalf("want 2 gpus, got %d", len(gpus))
	}
	if gpus[0].Index != 0 || gpus[0].Model != "NVIDIA GeForce RTX 5090 D" {
		t.Errorf("gpu0 = %+v", gpus[0])
	}
	// bus id 规范化:大写前缀 → sysfs 小写全零路径
	if got := gpus[0].BusID; got != "0000:1b:00.0" {
		t.Errorf("normalize bus: got %q", got)
	}
}

func TestParseSmiOutput_ErrorRow(t *testing.T) {
	if _, err := ParseSmiOutput("0, GPU-x, GeForce\n"); err == nil {
		t.Fatal("want error on 3-field row, got nil")
	}
}

func TestReadNumaNode(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "bus/pci/devices/0000:1b:00.0")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "numa_node"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := ReadNumaNode(root, "0000:1b:00.0")
	if err != nil || n != 0 {
		t.Fatalf("want numa 0, got %d err %v", n, err)
	}
	// 缺失 → -1 + err
	if _, err := ReadNumaNode(root, "0000:99:00.0"); err == nil {
		t.Fatal("want error for missing numa_node")
	}
}

func TestNodeTopologyLabels_Homogeneous(t *testing.T) {
	gpus := []GPUInfo{
		{Index: 0, Model: "NVIDIA GeForce RTX 5090 D", NumaNode: 0},
		{Index: 1, Model: "NVIDIA GeForce RTX 5090 D", NumaNode: 0},
	}
	labels := NodeTopologyLabels(gpus)
	if labels[topology.LabelNUMADomain] != "0" {
		t.Errorf("numa label = %q, want 0", labels[topology.LabelNUMADomain])
	}
	if labels[topology.LabelGPUCount] != "2" {
		t.Errorf("count label = %q", labels[topology.LabelGPUCount])
	}
	if labels[topology.LabelGPUModel] == "" {
		t.Error("model label missing")
	}
}

func TestNodeTopologyLabels_HeterogeneousNuma(t *testing.T) {
	// 卡跨 NUMA 域:不写 numa 标签(Score 走"无标签→0 分"兜底,不用错数据打分)
	gpus := []GPUInfo{
		{Index: 0, Model: "A100", NumaNode: 0},
		{Index: 1, Model: "A100", NumaNode: 1},
	}
	labels := NodeTopologyLabels(gpus)
	if _, ok := labels[topology.LabelNUMADomain]; ok {
		t.Errorf("heterogeneous NUMA must omit numa label, got %q", labels[topology.LabelNUMADomain])
	}
}

func TestNodeTopologyLabels_NoGpus(t *testing.T) {
	labels := NodeTopologyLabels(nil)
	if labels[topology.LabelGPUCount] != "0" {
		t.Errorf("empty gpus count = %q", labels[topology.LabelGPUCount])
	}
	if len(labels) != 1 {
		t.Errorf("empty gpus should only have count label, got %v", labels)
	}
}
