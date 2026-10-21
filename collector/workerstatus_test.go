package collector

import "testing"

func TestWorkerStatus(t *testing.T) {
	for _, test := range []struct {
		busy, drained bool
		want          string
	}{
		{busy: false, drained: false, want: "idle"},
		{busy: true, drained: false, want: "busy"},
		{busy: true, drained: true, want: "draining"},
		{busy: false, drained: true, want: "drained"},
	} {
		if got := workerStatus(test.busy, test.drained); got != test.want {
			t.Errorf("workerStatus(busy=%t, drained=%t) = %q, want %q", test.busy, test.drained, got, test.want)
		}
	}
}
