package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type Config struct {
	Workflow          string  `json:"workflow,omitempty"`
	Address           string  `json:"address"`
	CollectionPrefix  string  `json:"collection_prefix"`
	Rows              int     `json:"rows"`
	Queries           int     `json:"queries"`
	Seed              int64   `json:"seed"`
	AnchorUnix        int64   `json:"anchor_unix"`
	DeletedFraction   float64 `json:"deleted_fraction"`
	SubredditFraction float64 `json:"subreddit_fraction"`
	FlightFraction    float64 `json:"flight_fraction"`
	FlightIDs         []int64 `json:"flight_ids"`
	NList             int     `json:"nlist"`
	NProbe            int     `json:"nprobe"`
	HNSWM             int     `json:"hnsw_m"`
	EfConstruction    int     `json:"ef_construction"`
	Ef                int     `json:"ef"`
	PRQNRQ            int     `json:"prq_nrq"`
	PRQM              int     `json:"prq_m"`
	PRQNBits          int     `json:"prq_nbits"`
	QPS               int     `json:"qps"`
	Concurrency       int     `json:"concurrency"`
	WarmupSeconds     int     `json:"warmup_seconds"`
	DurationSeconds   int     `json:"duration_seconds"`
	TimeoutSeconds    int     `json:"timeout_seconds"`
	TopK              int     `json:"top_k"`
	BatchRows         int     `json:"batch_rows"`
}

func defaultConfig() Config {
	return Config{Address: "127.0.0.1:19530", CollectionPrefix: "cpu_bench", Rows: 100000, Queries: 100, Seed: 42,
		AnchorUnix: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), DeletedFraction: 0.05,
		SubredditFraction: 0.01, FlightFraction: 0.01, FlightIDs: []int64{1, 2, 3}, NList: 256, NProbe: 16,
		HNSWM: 16, EfConstruction: 200, Ef: 128, PRQNRQ: 2, PRQM: 32, PRQNBits: 8,
		QPS: 100, Concurrency: 32, WarmupSeconds: 10, DurationSeconds: 60, TimeoutSeconds: 10, TopK: 10, BatchRows: 1000}
}

func readConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return c, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("config must contain exactly one JSON object")
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.Rows < 1000 || c.Queries < 1 || c.NList < 1 || c.NList > c.Rows || c.NProbe < 1 || c.NProbe > c.NList || c.HNSWM < 2 || c.EfConstruction < 1 || c.Ef < 1 || c.PRQNRQ < 1 || c.PRQM < 1 || c.PRQNBits < 1 || c.QPS < 1 || c.Concurrency < 1 || c.DurationSeconds < 1 || c.TimeoutSeconds < 1 || c.TopK < 1 || c.BatchRows < 1 || c.BatchRows > 10000 || c.WarmupSeconds < 0 {
		return fmt.Errorf("invalid positive benchmark setting")
	}
	if c.DeletedFraction < 0 || c.DeletedFraction >= 1 || c.SubredditFraction <= 0 || c.SubredditFraction >= 1 || c.FlightFraction <= 0 || c.FlightFraction >= 1 || len(c.FlightIDs) == 0 {
		return fmt.Errorf("invalid filter distribution")
	}
	if c.Address == "" || c.CollectionPrefix == "" || c.AnchorUnix <= 0 {
		return fmt.Errorf("missing connection, collection, or anchor")
	}
	if c.QPS > 1000000 || c.DurationSeconds > 86400 || c.WarmupSeconds > 86400 || c.TimeoutSeconds > 86400 {
		return fmt.Errorf("qps must be <= 1000000 and durations must be <= 86400 seconds")
	}
	if c.TopK > c.Rows || c.Ef < c.TopK || 384%c.PRQM != 0 {
		return fmt.Errorf("top_k must be <= rows, ef must be >= top_k, and prq_m must divide 384")
	}
	return nil
}

type Case struct{ Name, Index, Filter string }

func selectedCases(c Config, name string) ([]Case, error) {
	available := cases(c)
	if name == "" {
		return available, nil
	}
	for _, cs := range available {
		if cs.Name == name {
			return []Case{cs}, nil
		}
	}
	names := make([]string, len(available))
	for i, cs := range available {
		names[i] = cs.Name
	}
	return nil, fmt.Errorf("case %q is not in %s; choose %s", name, c.Workflow, strings.Join(names, ", "))
}

func workflowIndex(workflow string) (string, error) {
	switch workflow {
	case "hnsw":
		return "hnsw512", nil
	case "prq":
		return "prq384", nil
	case "ivf":
		return "ivf128", nil
	default:
		return "", fmt.Errorf("unknown workflow %q (choose hnsw, prq, or ivf)", workflow)
	}
}

func cases(c Config) []Case {
	cutoff := c.AnchorUnix - 7*24*3600
	wide := []struct{ name, filter string }{{"not_deleted", "is_deleted == false"}, {"recent", fmt.Sprintf("created_time > %d", cutoff)}, {"not_deleted_recent", fmt.Sprintf("is_deleted == false and created_time > %d", cutoff)}}
	var out []Case
	if c.Workflow == "hnsw" || c.Workflow == "prq" {
		idx, _ := workflowIndex(c.Workflow)
		for _, w := range wide {
			out = append(out, Case{idx + "_" + w.name, idx, w.filter})
		}
		out = append(out, Case{idx + "_subreddit", idx, "subreddit_id == 1"})
	}
	if c.Workflow == "ivf" {
		ids := make([]string, len(c.FlightIDs))
		for i, id := range c.FlightIDs {
			ids[i] = fmt.Sprint(id)
		}
		f := fmt.Sprintf("flight_id in [%s]", strings.Join(ids, ","))
		out = append(out, Case{"ivf_flights", "ivf128", f}, Case{"ivf_flights_not_deleted", "ivf128", f + " and is_deleted == false"})
	}
	return out
}
