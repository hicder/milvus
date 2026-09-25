package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func report(inputs, labels string) error {
	dirs := strings.Split(inputs, ",")
	names := strings.Split(labels, ",")
	if inputs == "" || labels == "" || len(dirs) != len(names) || len(dirs) < 2 {
		return fmt.Errorf("report needs -inputs dir1,dir2 and -labels host1,host2")
	}
	type item struct {
		label  string
		result RunResult
	}
	rows := map[string][]item{}
	hash := ""
	for i, dir := range dirs {
		dir = strings.TrimSpace(dir)
		names[i] = strings.TrimSpace(names[i])
		if dir == "" || names[i] == "" {
			return fmt.Errorf("report inputs and labels must not be empty")
		}
		manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			return err
		}
		var manifest Manifest
		if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "manifest.json" || entry.Name() == "server-metadata.json" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				return err
			}
			var result RunResult
			if err = json.Unmarshal(b, &result); err != nil {
				return err
			}
			if result.Case == "" || result.DatasetSHA256 == "" {
				return fmt.Errorf("%s is not a result file", entry.Name())
			}
			if result.DatasetSHA256 != manifest.DataSHA256 {
				return fmt.Errorf("%s is from a previous dataset", filepath.Join(dir, entry.Name()))
			}
			if result.Config.QPS <= 0 || result.Config.DurationSeconds <= 0 {
				return fmt.Errorf("%s has no recorded run settings", entry.Name())
			}
			if hash == "" {
				hash = result.DatasetSHA256
			} else if hash != result.DatasetSHA256 {
				return fmt.Errorf("dataset checksums differ")
			}
			if seen[result.Case] {
				return fmt.Errorf("duplicate case %s in %s", result.Case, dir)
			}
			seen[result.Case] = true
			rows[result.Case] = append(rows[result.Case], item{names[i], result})
		}
		if len(seen) == 0 {
			return fmt.Errorf("no results in %s", dir)
		}
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(rows[k]) != len(dirs) {
			return fmt.Errorf("case %s missing from a host", k)
		}
		base := rows[k][0].result
		for _, v := range rows[k] {
			r := v.result
			if !comparableRuns(r, base) {
				return fmt.Errorf("case %s has mismatched run/search settings or server version", k)
			}
		}
	}
	fmt.Printf("Dataset SHA-256: `%s`\n\n| Case | Host | Offered QPS | Achieved QPS | p50 ms | p95 ms | p99 ms | p99.9 ms | Recall@k | Failed | Dropped |\n|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n", hash)
	for _, k := range keys {
		for _, v := range rows[k] {
			r := v.result
			fmt.Printf("| %s | %s | %d | %.1f | %.1f | %.1f | %.1f | %s | %.3f | %d | %d |\n", k, v.label, r.Config.QPS, r.AchievedQPS, r.P50MS, r.P95MS, r.P99MS, formatP999(r.P999MS), r.RecallAtK, r.Failed, r.Dropped)
		}
	}
	return nil
}

func comparableRuns(a, b RunResult) bool {
	a.Config.Address, b.Config.Address = "", ""
	a.Config.CollectionPrefix, b.Config.CollectionPrefix = "", ""
	return reflect.DeepEqual(a.Config, b.Config) && a.ServerVersion == b.ServerVersion
}
