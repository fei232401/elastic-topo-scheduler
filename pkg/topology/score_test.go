package topology

import "testing"

// helper:构造 NodeScoreInput
func input(groupSize, podsOnNode int, gpuCount, occupied int) NodeScoreInput {
	return NodeScoreInput{
		Node:            NodeTopology{NUMADomain: "numa0", GPUCount: gpuCount},
		Group:           Group{Name: "g", Size: groupSize},
		GroupPodsOnNode: podsOnNode,
		OccupiedGPUs:    occupied,
	}
}

func TestProximity(t *testing.T) {
	cases := []struct {
		name string
		in   NodeScoreInput
		want float64
	}{
		{"半满", input(4, 2, 8, 0), 0.5},
		{"全满", input(4, 4, 8, 0), 1.0},
		{"超预期截断到1", input(4, 6, 8, 0), 1.0},
		{"空组无约束", input(0, 0, 8, 0), 1.0},
		{"空节点驻留0", input(4, 0, 8, 0), 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Proximity(tc.in); got != tc.want {
				t.Errorf("Proximity() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompactness(t *testing.T) {
	cases := []struct {
		name string
		in   NodeScoreInput
		want float64
	}{
		{"全空", input(4, 0, 8, 0), 1.0},
		{"用1/4", input(4, 0, 8, 2), 0.75},
		{"全占", input(4, 0, 8, 8), 0.0},
		{"超占截断到0", input(4, 0, 8, 10), 0.0},
		{"零卡防御", input(4, 0, 0, 0), 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Compactness(tc.in); got != tc.want {
				t.Errorf("Compactness() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTotalScore_WeightedSum(t *testing.T) {
	// 邻近 0.5,碎片 0.75 → 0.7*0.5 + 0.3*0.75 = 0.35 + 0.225 = 0.575
	in := input(4, 2, 8, 2)
	got, feasible := TotalScore(in, ScoringParams{})
	if !feasible {
		t.Fatal("expected feasible, got false")
	}
	want := 0.7*0.5 + 0.3*0.75
	if got != want {
		t.Errorf("TotalScore() = %v, want %v", got, want)
	}
}

func TestTotalScore_FragmentationCap(t *testing.T) {
	// cap=0.5,8卡占6卡(剩余0.25<0.5)→ 不可用
	in := input(4, 0, 8, 6)
	_, feasible := TotalScore(in, ScoringParams{FragmentationCap: 0.5})
	if feasible {
		t.Fatal("expected infeasible under fragmentation cap, got feasible")
	}
	// cap=0(关闭)→ 可用
	if _, feasible := TotalScore(in, ScoringParams{}); !feasible {
		t.Fatal("expected feasible with cap disabled")
	}
	// cap 但剩余刚好够 → 可用
	in2 := input(4, 0, 8, 4) // 剩余0.5 >= 0.5
	if _, feasible := TotalScore(in2, ScoringParams{FragmentationCap: 0.5}); !feasible {
		t.Fatal("expected feasible when free == cap")
	}
	// 零卡节点在启用 cap 时不可用(避免除零)
	in3 := input(4, 0, 0, 0)
	if _, feasible := TotalScore(in3, ScoringParams{FragmentationCap: 0.5}); feasible {
		t.Fatal("expected infeasible for zero-GPU node under cap")
	}
}

func TestTotalScore_HigherIsBetter(t *testing.T) {
	// 同样组,已驻留更多的节点分更高(邻近度主导,权重0.7)
	sparse := input(4, 1, 8, 0)
	dense := input(4, 3, 8, 0)
	s1, _ := TotalScore(sparse, ScoringParams{})
	s2, _ := TotalScore(dense, ScoringParams{})
	if !(s2 > s1) {
		t.Errorf("dense proximity should score higher: sparse=%v dense=%v", s1, s2)
	}
}
