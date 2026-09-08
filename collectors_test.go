package main

import (
	"context"
	"slices"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/spacelift-io/prometheus-exporter/collector"
)

func collectorNames(collectors []collector.Collector) []string {
	out := make([]string, 0, len(collectors))
	for _, c := range collectors {
		out = append(out, c.Name())
	}

	return out
}

func TestNewCollectorsHonoursDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled map[string]bool
		want    []string
	}{
		{
			name: "defaults",
			want: []string{"publicworkerpool", "workerpools", "usage", "aggregates"},
		},
		{
			name:    "disable one",
			enabled: map[string]bool{"usage": false},
			want:    []string{"publicworkerpool", "workerpools", "aggregates"},
		},
		{
			name: "disable all",
			enabled: map[string]bool{
				"publicworkerpool": false, "workerpools": false, "usage": false, "aggregates": false,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := collectorNames(newCollectors(test.enabled)); !slices.Equal(got, test.want) {
				t.Fatalf("collector names = %v, want %v", got, test.want)
			}
		})
	}
}

// TestCollectorFlagsDisableCollectors runs the real flag set through urfave/cli
// so the node_exporter idiom (--collector.<name> / --no-collector.<name>) and
// its environment variable both reach newCollectors as intended.
func TestCollectorFlagsDisableCollectors(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		env  map[string]string
		want []string
	}{
		{
			name: "defaults",
			want: []string{"publicworkerpool", "workerpools", "usage", "aggregates"},
		},
		{
			name: "inverse flag",
			args: []string{"--no-collector.usage"},
			want: []string{"publicworkerpool", "workerpools", "aggregates"},
		},
		{
			name: "environment variable",
			env:  map[string]string{"SPACELIFT_PROMEX_COLLECTOR_AGGREGATES": "false"},
			want: []string{"publicworkerpool", "workerpools", "usage"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}

			var got []string
			command := &cli.Command{
				Name:  "test",
				Flags: collectorCLIFlags(),
				Action: func(_ context.Context, cmd *cli.Command) error {
					got = collectorNames(newCollectors(collectorSelection(cmd)))
					return nil
				},
			}
			if err := command.Run(context.Background(), append([]string{"test"}, test.args...)); err != nil {
				t.Fatalf("running command: %v", err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("enabled collectors = %v, want %v", got, test.want)
			}
		})
	}
}
