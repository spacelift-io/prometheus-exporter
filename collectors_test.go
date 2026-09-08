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

func TestNewCollectorsUsesStableDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled map[string]bool
		want    []string
	}{
		{
			name: "defaults",
			want: []string{"aggregates", "publicworkerpool", "usage", "workerpools"},
		},
		{
			name:    "disable one",
			enabled: map[string]bool{"usage": false},
			want:    []string{"aggregates", "publicworkerpool", "workerpools"},
		},
		{
			name: "disable all",
			enabled: map[string]bool{
				"aggregates":       false,
				"publicworkerpool": false,
				"usage":            false,
				"workerpools":      false,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := newCollectors(test.enabled)
			if names := collectorNames(got); !slices.Equal(names, test.want) {
				t.Fatalf("collector names = %v, want %v", names, test.want)
			}
		})
	}
}

func TestCollectorFlagsUseNodeExporterSyntax(t *testing.T) {
	flags := collectorCLIFlags()
	if len(flags) != len(collectorSpecs) {
		t.Fatalf("collector flags = %d, want %d", len(flags), len(collectorSpecs))
	}

	for index, spec := range collectorSpecs {
		flag, ok := flags[index].(*cli.BoolWithInverseFlag)
		if !ok {
			t.Fatalf("collector flag %q has type %T, want *cli.BoolWithInverseFlag", spec.name, flags[index])
		}
		want := []string{"collector." + spec.name, "no-collector." + spec.name}
		if names := flag.Names(); !slices.Equal(names, want) {
			t.Fatalf("flag names = %v, want %v", names, want)
		}
	}
}

func TestCollectorInverseFlagDisablesCollector(t *testing.T) {
	var selection map[string]bool
	command := &cli.Command{
		Name:  "test",
		Flags: collectorCLIFlags(),
		Action: func(_ context.Context, cmd *cli.Command) error {
			selection = collectorSelection(cmd)
			return nil
		},
	}

	if err := command.Run(context.Background(), []string{"test", "--no-collector.usage"}); err != nil {
		t.Fatalf("running command: %v", err)
	}
	if selection["usage"] {
		t.Fatal("--no-collector.usage did not disable the usage collector")
	}
	for _, name := range []string{"aggregates", "publicworkerpool", "workerpools"} {
		if !selection[name] {
			t.Fatalf("default collector %q was unexpectedly disabled", name)
		}
	}
}
