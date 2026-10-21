package collector

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, test := range []struct {
		name        string
		err         error
		unsupported bool
	}{
		{name: "nil", err: nil},
		{name: "genuine failure", err: errors.New("Message: internal error, Locations: []")},
		{name: "machine session", err: errors.New("Message: not available for machine sessions, Locations: []"), unsupported: true},
		{
			name:        "field missing from this schema",
			err:         errors.New(`Message: Cannot query field "searchSchedulableRuns" on type "WorkerPool"., Locations: []`),
			unsupported: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classify(test.err)

			if test.err == nil {
				if got != nil {
					t.Fatalf("classify(nil) = %v, want nil", got)
				}
				return
			}

			if errors.Is(got, ErrNotSupported) != test.unsupported {
				t.Errorf("classify(%q) unsupported = %t, want %t", test.err, !test.unsupported, test.unsupported)
			}
			if !errors.Is(got, ErrNotSupported) && got != test.err {
				t.Errorf("classify(%q) = %v, want the error unchanged", test.err, got)
			}
		})
	}
}
