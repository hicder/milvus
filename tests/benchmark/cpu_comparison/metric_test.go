package main

import (
	"testing"

	"github.com/milvus-io/milvus/client/v2/entity"
)

func TestExactTopKUsesWorkflowMetricAndFilter(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		cs       Case
		wantID   int64
		metric   entity.MetricType
	}{
		{"hnsw", Case{Name: "hnsw512_not_deleted", Index: "hnsw512"}, 1, entity.IP},
		{"prq", Case{Name: "prq384_not_deleted", Index: "prq384"}, 1, entity.IP},
		{"ivf", Case{Name: "ivf_flights_not_deleted", Index: "ivf100"}, 2, entity.L2},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			c := defaultConfig()
			c.Workflow, c.TopK = tc.workflow, 1
			// IP prefers the larger dot product; L2 prefers the identical vector.
			// The deleted row would win IP if the filter were ignored.
			d := Dataset{Config: c, Q512: [][]float32{{1, 0}}, Q384: [][]float32{{1, 0}}, Q100: [][]float32{{1, 0}}}
			for i, v := range [][]float32{{10, 0}, {1, 0}, {20, 0}} {
				d.Rows = append(d.Rows, Row{ID: int64(i + 1), Deleted: i == 2, Flight: c.FlightIDs[0], V512: v, V384: v, V100: v})
			}
			got, err := exactTopK(d, tc.cs)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !got[tc.wantID] {
				t.Fatalf("top-k IDs = %v, want %d", got, tc.wantID)
			}
			if metric := vectorIndex(c, tc.cs.Index).Params()["metric_type"]; metric != string(tc.metric) {
				t.Fatalf("server metric = %q, exact ranking expects %q", metric, tc.metric)
			}
		})
	}
}
