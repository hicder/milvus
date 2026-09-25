package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
)

type Row struct {
	ID               int64
	Deleted          bool
	Created          int64
	Subreddit        int64
	Flight           int64
	V512, V384, V128 []float32
}
type Dataset struct {
	Config           Config
	Rows             []Row
	Q512, Q384, Q128 [][]float32
}
type Manifest struct {
	Config     Config         `json:"config"`
	DataSHA256 string         `json:"data_sha256"`
	Counts     map[string]int `json:"filter_counts"`
}

func randomVector(r *rand.Rand, n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = r.Float32()*2 - 1
	}
	return v
}
func generate(c Config) Dataset {
	stop := progress("generating " + c.Workflow + " dataset")
	defer stop()
	r := rand.New(rand.NewSource(c.Seed))
	d := Dataset{Config: c, Rows: make([]Row, c.Rows)}
	idx, _ := workflowIndex(c.Workflow)
	selectedSub := int(float64(c.Rows) * c.SubredditFraction)
	selectedFlight := int(float64(c.Rows) * c.FlightFraction)
	deleted := int(float64(c.Rows) * c.DeletedFraction)
	for i := range d.Rows {
		d.Rows[i] = Row{ID: int64(i + 1), Deleted: i < deleted, Created: c.AnchorUnix - int64((i%14)*86400), Subreddit: int64(2 + i%997), Flight: int64(100 + i%997)}
		// Background IDs must not accidentally match custom flight_ids.
		for slices.Contains(c.FlightIDs, d.Rows[i].Flight) {
			d.Rows[i].Flight++
		}
		switch idx {
		case "hnsw512":
			d.Rows[i].V512 = randomVector(r, 512)
		case "prq384":
			d.Rows[i].V384 = randomVector(r, 384)
		case "ivf128":
			d.Rows[i].V128 = randomVector(r, 128)
		}
		if i < selectedSub {
			d.Rows[i].Subreddit = 1
		}
		if i < selectedFlight {
			d.Rows[i].Flight = c.FlightIDs[i%len(c.FlightIDs)]
		}
	}
	// Scatter scalar assignments independently of vector order, preserving exact counts.
	r.Shuffle(len(d.Rows), func(i, j int) { d.Rows[i].Deleted, d.Rows[j].Deleted = d.Rows[j].Deleted, d.Rows[i].Deleted })
	r.Shuffle(len(d.Rows), func(i, j int) { d.Rows[i].Created, d.Rows[j].Created = d.Rows[j].Created, d.Rows[i].Created })
	r.Shuffle(len(d.Rows), func(i, j int) { d.Rows[i].Subreddit, d.Rows[j].Subreddit = d.Rows[j].Subreddit, d.Rows[i].Subreddit })
	r.Shuffle(len(d.Rows), func(i, j int) { d.Rows[i].Flight, d.Rows[j].Flight = d.Rows[j].Flight, d.Rows[i].Flight })
	for i := 0; i < c.Queries; i++ {
		switch idx {
		case "hnsw512":
			d.Q512 = append(d.Q512, randomVector(r, 512))
		case "prq384":
			d.Q384 = append(d.Q384, randomVector(r, 384))
		case "ivf128":
			d.Q128 = append(d.Q128, randomVector(r, 128))
		}
	}
	return d
}
func match(row Row, cs Case, c Config) bool {
	switch cs.Name {
	case "hnsw512_not_deleted", "prq384_not_deleted":
		return !row.Deleted
	case "hnsw512_recent", "prq384_recent":
		return row.Created > c.AnchorUnix-7*86400
	case "hnsw512_not_deleted_recent", "prq384_not_deleted_recent":
		return !row.Deleted && row.Created > c.AnchorUnix-7*86400
	case "hnsw512_subreddit", "prq384_subreddit":
		return row.Subreddit == 1
	case "ivf_flights", "ivf_flights_not_deleted":
		ok := false
		for _, id := range c.FlightIDs {
			if row.Flight == id {
				ok = true
				break
			}
		}
		return ok && (cs.Name == "ivf_flights" || !row.Deleted)
	}
	return false
}
func saveDataset(dir string, d Dataset) (Manifest, error) {
	stop := progress("saving " + d.Config.Workflow + " dataset")
	defer stop()
	m := Manifest{Config: d.Config, Counts: map[string]int{}}
	for _, cs := range cases(d.Config) {
		for _, row := range d.Rows {
			if match(row, cs, d.Config) {
				m.Counts[cs.Name]++
			}
		}
		if m.Counts[cs.Name] == 0 {
			return m, fmt.Errorf("%s has no eligible rows; increase rows or filter survival fraction", cs.Name)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Manifest{}, err
	}
	path := filepath.Join(dir, "dataset.gob")
	f, err := os.CreateTemp(dir, ".dataset-*")
	if err != nil {
		return Manifest{}, err
	}
	defer os.Remove(f.Name())
	hash := sha256.New()
	err = gob.NewEncoder(io.MultiWriter(f, hash)).Encode(d)
	closeErr := f.Close()
	if err != nil {
		return Manifest{}, err
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return m, err
	}
	m.DataSHA256 = hex.EncodeToString(hash.Sum(nil))
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	return m, os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0644)
}
func loadDataset(dir string) (Dataset, Manifest, error) {
	stop := progress("reading/checking dataset")
	defer stop()
	var d Dataset
	var m Manifest
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return d, m, err
	}
	if err = json.Unmarshal(mb, &m); err != nil {
		return d, m, err
	}
	f, err := os.Open(filepath.Join(dir, "dataset.gob"))
	if err != nil {
		return d, m, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return d, m, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != m.DataSHA256 {
		return d, m, fmt.Errorf("dataset checksum mismatch")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return d, m, err
	}
	if err = gob.NewDecoder(f).Decode(&d); err != nil {
		return d, m, err
	}
	if !reflect.DeepEqual(d.Config, m.Config) {
		return d, m, fmt.Errorf("manifest config does not match the checksummed dataset")
	}
	if err = d.Config.validate(); err != nil {
		return d, m, fmt.Errorf("dataset config: %w", err)
	}
	idx, err := workflowIndex(d.Config.Workflow)
	if err != nil {
		return d, m, err
	}
	var queries int
	switch idx {
	case "hnsw512":
		queries = len(d.Q512)
	case "prq384":
		queries = len(d.Q384)
	case "ivf128":
		queries = len(d.Q128)
	}
	if len(d.Rows) != m.Config.Rows || queries != m.Config.Queries || d.Config.Workflow != m.Config.Workflow {
		return d, m, fmt.Errorf("dataset shape mismatch")
	}
	for _, row := range d.Rows {
		vector := rowVector(row, idx)
		if len(vector) != dimension(idx) {
			return d, m, fmt.Errorf("row %d has wrong vector dimension", row.ID)
		}
	}
	for i := 0; i < queries; i++ {
		if len(queryVector(d, idx, i)) != dimension(idx) {
			return d, m, fmt.Errorf("query %d has wrong vector dimension", i)
		}
	}
	return d, m, nil
}

func rowVector(row Row, idx string) []float32 {
	switch idx {
	case "hnsw512":
		return row.V512
	case "prq384":
		return row.V384
	default:
		return row.V128
	}
}
