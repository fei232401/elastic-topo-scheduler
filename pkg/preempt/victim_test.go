package preempt

import "testing"

func TestLessVictim_Ordering(t *testing.T) {
	cases := []struct {
		name string
		a, b VictimCandidate
		want bool // 期望 a 先被抢(a 损失更小)
	}{
		{
			name: "低优先级优先",
			a:    VictimCandidate{PodKey: "low", Priority: 1},
			b:    VictimCandidate{PodKey: "high", Priority: 9},
			want: true,
		},
		{
			name: "同优先级下有checkpoint优先",
			a:    VictimCandidate{PodKey: "cp", Priority: 5, HasCheckpoint: true},
			b:    VictimCandidate{PodKey: "ncp", Priority: 5, HasCheckpoint: false},
			want: true,
		},
		{
			name: "同优先级同checkpoint:剩余长优先被抢(损失最小)",
			a:    VictimCandidate{PodKey: "long", Priority: 5, HasCheckpoint: true, RemainingRuntime: 3600},
			b:    VictimCandidate{PodKey: "short", Priority: 5, HasCheckpoint: true, RemainingRuntime: 60},
			want: true,
		},
		{
			name: "优先级高但有checkpoint也不能插队",
			a:    VictimCandidate{PodKey: "lo", Priority: 1, HasCheckpoint: true},
			b:    VictimCandidate{PodKey: "hi", Priority: 9, HasCheckpoint: false},
			want: true,
		},
		{
			name: "完全等价返回false(不先抢a)",
			a:    VictimCandidate{PodKey: "x", Priority: 5},
			b:    VictimCandidate{PodKey: "y", Priority: 5},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LessVictim(tc.a, tc.b); got != tc.want {
				t.Errorf("LessVictim(%q, %q) = %v, want %v", tc.a.PodKey, tc.b.PodKey, got, tc.want)
			}
		})
	}
}

func TestSortVictims_ExpectedOrder(t *testing.T) {
	in := []VictimCandidate{
		{PodKey: "mid-nocp", Priority: 5, HasCheckpoint: false, RemainingRuntime: 100},
		{PodKey: "high-cp", Priority: 9, HasCheckpoint: true, RemainingRuntime: 100},
		{PodKey: "low-cp-long", Priority: 1, HasCheckpoint: true, RemainingRuntime: 3600},
		{PodKey: "low-cp-short", Priority: 1, HasCheckpoint: true, RemainingRuntime: 60},
		{PodKey: "low-nocp", Priority: 1, HasCheckpoint: false, RemainingRuntime: 100},
	}
	got := SortVictims(in)
	want := []string{"low-cp-long", "low-cp-short", "low-nocp", "mid-nocp", "high-cp"}
	for i := range want {
		if got[i].PodKey != want[i] {
			t.Errorf("position %d = %q, want %q (full: %v)", i, got[i].PodKey, want[i], keys(got))
		}
	}
}

func TestSortVictims_DoesNotMutateInput(t *testing.T) {
	in := []VictimCandidate{
		{PodKey: "b", Priority: 5},
		{PodKey: "a", Priority: 1},
	}
	before := in[0].PodKey
	_ = SortVictims(in)
	if in[0].PodKey != before {
		t.Errorf("input was mutated: in[0]=%q, want %q", in[0].PodKey, before)
	}
}

func keys(vs []VictimCandidate) []string {
	out := make([]string, len(vs))
	for i := range vs {
		out[i] = vs[i].PodKey
	}
	return out
}
