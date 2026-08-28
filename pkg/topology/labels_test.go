package topology

import "testing"

func TestParseNodeTopology_Valid(t *testing.T) {
	labels := map[string]string{
		LabelNUMADomain: "numa0",
		LabelGPUCount:   "2",
		LabelGPUModel:   "4090",
	}
	nt, err := ParseNodeTopology(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nt.NUMADomain != "numa0" {
		t.Errorf("NUMADomain = %q, want %q", nt.NUMADomain, "numa0")
	}
	if nt.GPUCount != 2 {
		t.Errorf("GPUCount = %d, want 2", nt.GPUCount)
	}
	if nt.GPUModel != "4090" {
		t.Errorf("GPUModel = %q, want %q", nt.GPUModel, "4090")
	}
}

func TestParseNodeTopology_MissingNUMADomain(t *testing.T) {
	labels := map[string]string{LabelGPUCount: "2"}
	if _, err := ParseNodeTopology(labels); err == nil {
		t.Fatal("expected error for missing topo.numa, got nil")
	}
}

func TestParseNodeTopology_MissingGPUCount(t *testing.T) {
	labels := map[string]string{LabelNUMADomain: "numa0"}
	if _, err := ParseNodeTopology(labels); err == nil {
		t.Fatal("expected error for missing gpu.count, got nil")
	}
}

func TestParseNodeTopology_InvalidGPUCount(t *testing.T) {
	labels := map[string]string{LabelNUMADomain: "numa0", LabelGPUCount: "abc"}
	if _, err := ParseNodeTopology(labels); err == nil {
		t.Fatal("expected error for invalid gpu.count, got nil")
	}
}

func TestParseNodeTopology_DefaultGPUModel(t *testing.T) {
	labels := map[string]string{LabelNUMADomain: "numa0", LabelGPUCount: "1"}
	nt, err := ParseNodeTopology(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nt.GPUModel != "unknown" {
		t.Errorf("GPUModel = %q, want %q", nt.GPUModel, "unknown")
	}
}
