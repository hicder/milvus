package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

func TestFlightIDsDoNotMatchBackground(t *testing.T) {
	c := defaultConfig()
	c.Workflow, c.Rows, c.Queries = "ivf", 1000, 1
	c.FlightIDs = []int64{100, 101, 102, 500}
	m, err := saveDataset(t.TempDir(), generate(c))
	if err != nil {
		t.Fatal(err)
	}
	if m.Counts["ivf_flights"] != 10 {
		t.Fatalf("unexpected survival: %v", m.Counts)
	}
}

func TestManifestConfigMustMatchDataset(t *testing.T) {
	c := defaultConfig()
	c.Workflow, c.Rows, c.Queries = "hnsw", 1000, 1
	dir := t.TempDir()
	m, err := saveDataset(dir, generate(c))
	if err != nil {
		t.Fatal(err)
	}
	m.Config.CollectionPrefix = "another_collection"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = loadDataset(dir); err == nil || !strings.Contains(err.Error(), "manifest config") {
		t.Fatalf("got %v", err)
	}
}

func TestConfigRejectsTrailingJSON(t *testing.T) {
	b, err := json.Marshal(defaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, append(b, []byte(" {}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readConfig(path); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}

func TestSearchResultErrors(t *testing.T) {
	want := errors.New("server search failed")
	if err := searchResultError([]milvusclient.ResultSet{{Err: want}}); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	for _, sets := range [][]milvusclient.ResultSet{nil, {{ResultCount: 0}}} {
		if searchResultError(sets) == nil {
			t.Fatal("accepted an empty search result")
		}
	}
}

func TestMeasureCancellationKeepsAccounting(t *testing.T) {
	c := defaultConfig()
	c.DurationSeconds, c.QPS = 1, 10
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := measure(ctx, c, func(context.Context, int) error { cancel(); return nil })
	if res.Successful != 1 || res.Successful+res.Failed+res.Dropped != res.Offered || res.P95MS <= 0 || res.P999MS == nil || *res.P999MS != res.P95MS {
		t.Fatalf("invalid interrupted result: %+v", res)
	}
}

func TestPercentileP999(t *testing.T) {
	latencies := make([]float64, 1000)
	for i := range latencies {
		latencies[i] = float64(i + 1)
	}
	if got := percentile(latencies, .999); got != 999 {
		t.Fatalf("p99.9 = %v, want 999", got)
	}
	if got := formatP999(nil); got != "n/a" {
		t.Fatalf("missing p99.9 = %q", got)
	}
}

func TestMeasureIncludesDrainTime(t *testing.T) {
	c := defaultConfig()
	c.DurationSeconds, c.QPS = 1, 1
	res := measure(context.Background(), c, func(context.Context, int) error { time.Sleep(1100 * time.Millisecond); return nil })
	if res.Successful != 1 || res.ElapsedSeconds < 1.1 || res.AchievedQPS >= 1 {
		t.Fatalf("drain time omitted: %+v", res)
	}
}

func TestMeasureDropsWhenFullAndRecordsFailures(t *testing.T) {
	c := defaultConfig()
	c.DurationSeconds, c.QPS, c.Concurrency = 1, 100, 1
	res := measure(context.Background(), c, func(context.Context, int) error { time.Sleep(30 * time.Millisecond); return errors.New("test failure") })
	if res.Dropped == 0 || res.Failed == 0 || res.Dropped+res.Failed != 100 || res.Errors[0] != "test failure" {
		t.Fatalf("invalid failure/drop accounting: %+v", res)
	}
}

func TestReportRejectsDifferentRunControls(t *testing.T) {
	base := RunResult{Config: defaultConfig(), ServerVersion: "2.6.24"}
	for _, change := range []func(*RunResult){
		func(r *RunResult) { r.Config.Concurrency++ },
		func(r *RunResult) { r.Config.WarmupSeconds++ },
		func(r *RunResult) { r.Config.TimeoutSeconds++ },
		func(r *RunResult) { r.Config.EfConstruction++ },
		func(r *RunResult) { r.ServerVersion = "different" },
	} {
		other := base
		change(&other)
		if comparableRuns(base, other) {
			t.Fatal("accepted mismatched run")
		}
	}
	other := base
	other.Config.Address = "another-host:19530"
	if !comparableRuns(base, other) {
		t.Fatal("rejected different host")
	}
}

func TestCLIRejectsIgnoredFlags(t *testing.T) {
	original := os.Args
	t.Cleanup(func() { os.Args = original })
	for _, args := range [][]string{
		{"setup", "-qps", "100"}, {"run", "-replace"}, {"run", "-config", "ignored.json"}, {"up", "-workflow", "hnsw"}, {"run", "unexpected"},
	} {
		os.Args = append([]string{"cpu_comparison"}, args...)
		if err := mainErr(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
