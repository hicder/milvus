package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatasetReproducibleAndFilters(t *testing.T) {
	c := defaultConfig()
	c.Workflow = "ivf"
	c.Rows = 1000
	c.Queries = 3
	c.SubredditFraction = .001
	c.FlightFraction = .01
	d := generate(c)
	if len(d.Rows) != c.Rows {
		t.Fatal("wrong row count")
	}
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	m1, err := saveDataset(first, d)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := saveDataset(second, generate(c))
	if err != nil {
		t.Fatal(err)
	}
	if m1.DataSHA256 != m2.DataSHA256 {
		t.Fatal("same seed produced different data")
	}
	if m1.Counts["ivf_flights"] != 10 || len(m1.Counts) != 2 {
		t.Fatalf("unexpected selectivity: %v", m1.Counts)
	}
	loaded, got, err := loadDataset(first)
	if err != nil {
		t.Fatal(err)
	}
	if got.DataSHA256 != m1.DataSHA256 || len(loaded.Q128) != 3 {
		t.Fatal("bundle round trip failed")
	}
	file := filepath.Join(first, "dataset.gob")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	if err = os.WriteFile(file, b, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = loadDataset(first); err == nil {
		t.Fatal("tampered dataset passed checksum validation")
	}
}

func TestWorkflowCreatesOnlyItsCasesAndVectors(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		count    int
		index    string
	}{
		{"hnsw", 4, "hnsw512"}, {"prq", 4, "prq384"},
		{"ivf", 2, "ivf128"},
	} {
		c := defaultConfig()
		c.Workflow = tc.workflow
		c.Rows = 1000
		c.Queries = 2
		c.SubredditFraction = .001
		d := generate(c)
		got := cases(c)
		if len(got) != tc.count {
			t.Errorf("%s cases=%d, want %d", tc.workflow, len(got), tc.count)
		}
		for _, cs := range got {
			if cs.Index != tc.index {
				t.Errorf("%s uses %s", tc.workflow, cs.Index)
			}
			if cs.Name == "hnsw512_subreddit" || cs.Name == "prq384_subreddit" {
				matches := 0
				for _, row := range d.Rows {
					if match(row, cs, c) {
						matches++
					}
				}
				if matches != 1 {
					t.Errorf("%s 0.1 percent matches=%d, want 1", tc.workflow, matches)
				}
			}
		}
		if len(queryVector(d, tc.index, 0)) != dimension(tc.index) {
			t.Errorf("%s wrong vector dimension", tc.workflow)
		}
		if tc.index != "hnsw512" && len(d.Q512) != 0 || tc.index != "prq384" && len(d.Q384) != 0 || tc.index != "ivf128" && len(d.Q128) != 0 {
			t.Errorf("%s generated an unselected vector set", tc.workflow)
		}
	}
}

func TestSelectedCases(t *testing.T) {
	c := defaultConfig()
	c.Workflow = "hnsw"
	selected, err := selectedCases(c, "hnsw512_recent")
	if err != nil || len(selected) != 1 || selected[0].Name != "hnsw512_recent" {
		t.Fatalf("selected=%v, err=%v", selected, err)
	}
	all, err := selectedCases(c, "")
	if err != nil || len(all) != 4 {
		t.Fatalf("all=%v, err=%v", all, err)
	}
	if _, err = selectedCases(c, "ivf_flights"); err == nil {
		t.Fatal("accepted a case from another workflow")
	}
}
