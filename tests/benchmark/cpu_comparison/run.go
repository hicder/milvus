package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

type RunResult struct {
	Config              Config   `json:"config"`
	Case                string   `json:"case"`
	ServerVersion       string   `json:"server_version"`
	DatasetSHA256       string   `json:"dataset_sha256"`
	Offered             int      `json:"offered"`
	Successful          int      `json:"successful"`
	Failed              int      `json:"failed"`
	Dropped             int      `json:"dropped"`
	ElapsedSeconds      float64  `json:"elapsed_seconds"`
	AchievedQPS         float64  `json:"achieved_qps"`
	P50MS               float64  `json:"p50_ms"`
	P95MS               float64  `json:"p95_ms"`
	P99MS               float64  `json:"p99_ms"`
	P999MS              *float64 `json:"p999_ms,omitempty"`
	MaxSchedulerDelayMS float64  `json:"max_scheduler_delay_ms"`
	RecallAtK           float64  `json:"recall_at_k"`
	GOOS                string   `json:"goos"`
	GOARCH              string   `json:"goarch"`
	CPUs                int      `json:"cpus"`
	Errors              []string `json:"errors,omitempty"`
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[int(math.Ceil(float64(len(v))*p))-1]
}

// measure sends open-loop arrivals; a full in-flight limit drops an arrival.
func measure(ctx context.Context, c Config, request func(context.Context, int) error) RunResult {
	res := RunResult{Config: c, Offered: c.QPS * c.DurationSeconds}
	var mu sync.Mutex
	var latencies []float64
	sem := make(chan struct{}, c.Concurrency)
	var wg sync.WaitGroup
	start := time.Now()
	end := start.Add(time.Duration(c.DurationSeconds) * time.Second)
arrivals:
	for i := 0; i < res.Offered; i++ {
		// Split seconds and remainder to avoid truncation drift and overflow.
		scheduled := start.Add(time.Duration(i/c.QPS)*time.Second + time.Duration(i%c.QPS)*time.Second/time.Duration(c.QPS))
		if ctx.Err() != nil || !time.Now().Before(end) {
			res.Dropped += res.Offered - i
			break
		}
		if wait := time.Until(scheduled); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				res.Dropped += res.Offered - i
				break arrivals
			}
		}
		if ctx.Err() != nil || !time.Now().Before(end) {
			res.Dropped += res.Offered - i
			break
		}
		select {
		case sem <- struct{}{}:
		default:
			res.Dropped++
			continue
		}
		wg.Add(1)
		go func(n int, at time.Time) {
			defer wg.Done()
			defer func() { <-sem }()
			delay := time.Since(at).Seconds() * 1000
			requestCtx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
			begin := time.Now()
			err := request(requestCtx, n)
			elapsed := time.Since(begin).Seconds() * 1000
			cancel()
			mu.Lock()
			defer mu.Unlock()
			res.MaxSchedulerDelayMS = max(res.MaxSchedulerDelayMS, delay)
			if err != nil {
				res.Failed++
				if len(res.Errors) < 5 {
					res.Errors = append(res.Errors, err.Error())
				}
				return
			}
			res.Successful++
			latencies = append(latencies, elapsed)
		}(i, scheduled)
	}
	wg.Wait()
	// Include the configured arrival window and any time spent draining requests.
	if wait := time.Until(end); wait > 0 && ctx.Err() == nil {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
	res.ElapsedSeconds = time.Since(start).Seconds()
	sort.Float64s(latencies)
	res.AchievedQPS = float64(res.Successful) / res.ElapsedSeconds
	res.P50MS = percentile(latencies, .5)
	res.P95MS = percentile(latencies, .95)
	res.P99MS = percentile(latencies, .99)
	if len(latencies) != 0 {
		p999 := percentile(latencies, .999)
		res.P999MS = &p999
	}
	return res
}

func executeCase(ctx context.Context, cli *milvusclient.Client, d Dataset, m Manifest, cs Case) RunResult {
	stop := progress(fmt.Sprintf("%s requests (%ds window, then drain)", cs.Name, d.Config.DurationSeconds))
	defer stop()
	res := measure(ctx, d.Config, func(ctx context.Context, n int) error {
		sets, err := search(ctx, cli, d, cs, n)
		if err != nil {
			return err
		}
		return searchResultError(sets)
	})
	res.Case, res.DatasetSHA256 = cs.Name, m.DataSHA256
	res.GOOS, res.GOARCH, res.CPUs = runtime.GOOS, runtime.GOARCH, runtime.NumCPU()
	return res
}
func runAll(ctx context.Context, cli *milvusclient.Client, d Dataset, m Manifest, dir string, selected []Case) error {
	version, err := cli.GetServerVersion(ctx, milvusclient.NewGetServerVersionOption())
	if err != nil {
		return err
	}
	for _, cs := range selected {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Printf("%s: checking sample recall\n", cs.Name)
		stop := progress(cs.Name + " sample recall")
		recall, err := exactRecall(ctx, cli, d, cs)
		stop()
		if err != nil {
			return err
		}
		if d.Config.WarmupSeconds > 0 {
			warm := d
			warm.Config.DurationSeconds = d.Config.WarmupSeconds
			fmt.Printf("%s: warming up for %ds\n", cs.Name, warm.Config.DurationSeconds)
			warmResult := executeCase(ctx, cli, warm, m, cs)
			if warmResult.Failed > 0 {
				return fmt.Errorf("%s warmup: %s", cs.Name, warmResult.Errors[0])
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Printf("%s: measuring for %ds at %d QPS\n", cs.Name, d.Config.DurationSeconds, d.Config.QPS)
		res := executeCase(ctx, cli, d, m, cs)
		res.RecallAtK, res.ServerVersion = recall, version
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(dir+"/"+cs.Name+".json", b, 0644); err != nil {
			return err
		}
		fmt.Printf("%s: %.1f QPS, p50 %.1f ms, p95 %.1f ms, p99 %.1f ms, p99.9 %s ms, failed %d, dropped %d\n", cs.Name, res.AchievedQPS, res.P50MS, res.P95MS, res.P99MS, formatP999(res.P999MS), res.Failed, res.Dropped)
		if err := ctx.Err(); err != nil {
			return err
		}
		if res.Failed > 0 {
			return fmt.Errorf("%s: %d searches failed (results saved)", cs.Name, res.Failed)
		}
	}
	return nil
}

func formatP999(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", *v)
}
