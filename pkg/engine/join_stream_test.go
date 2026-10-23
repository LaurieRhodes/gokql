package engine

import (
	"fmt"
	"testing"
)

// The streamed join → summarize path must give what materialising the
// join gives. Tiny batches force many partial summaries and merges.
func TestJoinStreamMatchesMaterialised(t *testing.T) {
	src := `let L = range i from 1 to 300 step 1 | extend k = i % 7, v = i, s = strcat("s", tostring(i % 5)), d = datetime(2026-01-01) + i * 1h;
let R = range j from 1 to 40 step 1 | extend k = j % 7, w = j * 1.5;
`
	queries := []string{
		`L | join kind=inner R on k | summarize count() by s`,
		`L | join kind=inner R on k | where w > 10 | extend z = v * 2 | summarize n = count(), c = countif(v > 100), t = sum(z), lo = min(d), hi = max(w), sw = sumif(w, k == 3) by k, s`,
		`L | join kind=inner R on k | summarize count(), sum(v), max(s)`,
		`L | join kind=leftouter (R | where j < 5) on k | summarize count(), countif(isnull(j)) by k`,
		`L | join kind=fullouter (R | where j > 35) on k | project v, j | summarize count(), min(v), max(j)`,
		`L | join kind=inner R on k | where v < 0 | summarize count()`,
		`L | join kind=inner R on k | where v < 0 | summarize count() by k`,
		`L | join kind=inner R on k | summarize count() by s | order by s asc`,
	}
	run := func(q string) string {
		tbl := queryResult(t, src+q)
		out := fmt.Sprint(tbl.Schema.Columns, "\n")
		for _, r := range tbl.Rows {
			out += fmt.Sprintln(r...)
		}
		return out
	}
	defer func(m, b int) { streamJoinMinRows, streamJoinBatch = m, b }(streamJoinMinRows, streamJoinBatch)
	for _, q := range queries {
		streamJoinMinRows, streamJoinBatch = 1<<40, 1<<20
		want := run(q)
		for _, batch := range []int{1, 7, 1000, 1 << 20} {
			streamJoinMinRows, streamJoinBatch = 0, batch
			if got := run(q); got != want {
				t.Errorf("%s (batch %d):\n got  %s\n want %s", q, batch, got, want)
			}
		}
	}
}
