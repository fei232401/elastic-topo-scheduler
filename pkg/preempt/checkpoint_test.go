package preempt

import "testing"

func TestAnnotationCheckpoint_HasCheckpoint(t *testing.T) {
	c := NewAnnotationCheckpoint()
	c.Set("pod-a", "true")
	c.Set("pod-b", "false")
	c.Set("pod-c", "yes") // 非 "true" 一律视为无

	cases := []struct {
		pod  string
		want bool
	}{
		{"pod-a", true},
		{"pod-b", false},
		{"pod-c", false},
		{"pod-missing", false},
	}
	for _, tc := range cases {
		if got := c.HasCheckpoint(tc.pod); got != tc.want {
			t.Errorf("HasCheckpoint(%q) = %v, want %v", tc.pod, got, tc.want)
		}
	}
}

func TestAnnotationCheckpoint_InterfaceSatisfied(t *testing.T) {
	var _ CheckpointState = NewAnnotationCheckpoint()
}
