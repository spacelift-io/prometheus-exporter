package collector

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func pageOf(t *testing.T, body string) schedulableRuns {
	t.Helper()

	var runs schedulableRuns
	if err := json.Unmarshal([]byte(body), &runs); err != nil {
		t.Fatalf("decoding page: %v", err)
	}

	return runs
}

func TestSummarizeQueue(t *testing.T) {
	int64p := func(v int64) *int64 { return &v }

	const mixed = `{"edges":[
		{"node":{"run":{"createdAt":300,"state":"READY","type":"TRACKED"}}},
		{"node":{"run":{"createdAt":100,"state":"PLANNING","type":"PROPOSED"}}},
		{"node":{"run":{"createdAt":200,"state":"READY","type":"TRACKED"}}},
		{"node":{"run":{"createdAt":400,"state":"READY","type":"PROPOSED"}}}
	],"pageInfo":{"hasNextPage":false}}`
	mixedCounts := []runCount{
		{runKey: runKey{state: "PLANNING", runType: "PROPOSED"}, count: 1},
		{runKey: runKey{state: "READY", runType: "PROPOSED"}, count: 1},
		{runKey: runKey{state: "READY", runType: "TRACKED"}, count: 2},
	}

	for _, test := range []struct {
		name  string
		page  string
		total int
		want  queueSummary
	}{
		{
			name: "empty",
			page: `{"edges":[],"pageInfo":{"hasNextPage":false}}`,
			want: queueSummary{},
		},
		{
			name:  "complete: oldest found regardless of order",
			page:  mixed,
			total: 4,
			want:  queueSummary{counts: mixedCounts, oldest: int64p(100)},
		},
		{
			// The API's actual behaviour past its cap: fewer runs than
			// it matched, and hasNextPage false regardless.
			name:  "partial with hasNextPage false: truncated, oldest withheld",
			page:  mixed,
			total: 750,
			want:  queueSummary{counts: mixedCounts, truncated: true},
		},
		{
			name:  "partial by hasNextPage alone",
			page:  `{"edges":[{"node":{"run":{"createdAt":100,"state":"READY","type":"TRACKED"}}}],"pageInfo":{"hasNextPage":true}}`,
			total: 1,
			want: queueSummary{
				counts:    []runCount{{runKey: runKey{state: "READY", runType: "TRACKED"}, count: 1}},
				truncated: true,
			},
		},
		{
			// The total is counted separately and can lag the search by
			// a run that arrived between the two. More returned than
			// counted is still everything.
			name:  "more returned than counted",
			page:  mixed,
			total: 3,
			want:  queueSummary{counts: mixedCounts, oldest: int64p(100)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := summarizeQueue(pageOf(t, test.page), test.total); !reflect.DeepEqual(got, test.want) {
				t.Errorf("summarizeQueue = %+v, want %+v", got, test.want)
			}
		})
	}
}

// runsPage renders a search result of n READY runs, created one second apart
// from first, newest first so that the oldest is not simply the first edge.
// hasNextPage is false, as the API reports it whatever the result's size.
func runsPage(t *testing.T, n int, first int64) schedulableRuns {
	t.Helper()

	edges := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		edges = append(edges, fmt.Sprintf(`{"node":{"run":{"createdAt":%d,"state":"READY","type":"TRACKED"}}}`, first+int64(i)))
	}

	return pageOf(t, `{"edges":[`+strings.Join(edges, ",")+`],"pageInfo":{"hasNextPage":false}}`)
}

// TestSummarizeQueueBoundaries pins completeness on either side of a partial
// result. The search ignores the requested page size and reports hasNextPage
// false whatever it returns, so only the search's total says whether the
// result is whole. The sizes are arbitrary; what matters is one run short.
func TestSummarizeQueueBoundaries(t *testing.T) {
	const firstCreated = 1752700000

	for _, test := range []struct {
		name            string
		returned, total int
		truncated       bool
	}{
		{name: "100 of 100", returned: 100, total: 100},
		{name: "100 of 101", returned: 100, total: 101, truncated: true},
		{name: "101 of 101", returned: 101, total: 101},
		{name: "250 of 250", returned: 250, total: 250},
		{name: "250 of 251", returned: 250, total: 251, truncated: true},
		// A small fraction of a large queue: the returned runs can all be
		// newer than one that was not returned. Their minimum must not be
		// reported as the oldest.
		{name: "250 of 2500", returned: 250, total: 2500, truncated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			summary := summarizeQueue(runsPage(t, test.returned, firstCreated), test.total)

			if summary.truncated != test.truncated {
				t.Errorf("truncated = %t, want %t", summary.truncated, test.truncated)
			}

			counted := 0
			for _, c := range summary.counts {
				counted += c.count
			}
			if counted != test.returned {
				t.Errorf("counts cover %d runs, want every returned run (%d)", counted, test.returned)
			}

			switch {
			case test.truncated && summary.oldest != nil:
				t.Errorf("oldest = %d on a partial result, want it withheld", *summary.oldest)
			case !test.truncated && (summary.oldest == nil || *summary.oldest != firstCreated):
				t.Errorf("oldest = %v on a complete result, want %d", summary.oldest, int64(firstCreated))
			}
		})
	}
}
