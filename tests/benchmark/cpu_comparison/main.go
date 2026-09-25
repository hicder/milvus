package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

func main() {
	if err := mainErr(); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func mainErr() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: go run ./tests/benchmark/cpu_comparison <up|setup|run|reset|drop|down|report|init|metadata> [flags]")
	}
	cmd := os.Args[1]
	allowed, ok := map[string]string{
		"up": "", "down": "volumes", "init": "config", "report": "inputs labels",
		"setup": "config data workflow address replace", "reset": "config data workflow address",
		"run": "data workflow address case qps duration concurrency server-metadata", "drop": "data workflow address",
		"metadata": "output container server-config instance-type build-flags notes",
	}[cmd]
	if !ok {
		return fmt.Errorf("unknown command %s", cmd)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := fs.String("config", "tests/benchmark/cpu_comparison/config.json", "configuration file")
	dir := fs.String("data", "tests/benchmark/cpu_comparison/benchmark-data", "dataset and results directory")
	replace := fs.Bool("replace", false, "rebuild the selected workflow and replace its data bundle")
	volumes := fs.Bool("volumes", false, "down: also delete all data stored in the local Docker stack")
	workflow := fs.String("workflow", "", "one of hnsw, prq, ivf")
	caseName := fs.String("case", "", "run one case from the selected workflow")
	address := fs.String("address", "", "override Milvus address without regenerating data")
	qps := fs.Int("qps", 0, "override offered QPS for run")
	duration := fs.Int("duration", 0, "override measured seconds for run")
	concurrency := fs.Int("concurrency", 0, "override maximum in-flight requests for run")
	inputs := fs.String("inputs", "", "comma-separated result directories for report")
	labels := fs.String("labels", "", "comma-separated host labels for report")
	serverMetadataPath := fs.String("server-metadata", "", "attach metadata captured on the Linux Milvus server")
	var metadataOpts metadataOptions
	fs.StringVar(&metadataOpts.Output, "output", "", "metadata output file (must not exist)")
	fs.StringVar(&metadataOpts.Container, "container", "", "Milvus container ID/name on this host's local Docker daemon")
	fs.StringVar(&metadataOpts.ServerConfig, "server-config", "", "sanitized Milvus config file to embed verbatim; remove secrets first")
	fs.StringVar(&metadataOpts.InstanceType, "instance-type", "", "server instance type, supplied by operator")
	fs.StringVar(&metadataOpts.BuildFlags, "build-flags", "", "running server build flags, supplied by operator")
	fs.StringVar(&metadataOpts.Notes, "notes", "", "baseline/tuned label, binary provenance, native resource limits, or other context")
	validFlags := strings.Fields(allowed)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: go run ./tests/benchmark/cpu_comparison %s [flags]\n", cmd)
		help := flag.NewFlagSet(cmd, flag.ContinueOnError)
		help.SetOutput(fs.Output())
		fs.VisitAll(func(f *flag.Flag) {
			if slices.Contains(validFlags, f.Name) {
				help.Var(f.Value, f.Name, f.Usage)
			}
		})
		help.PrintDefaults()
	}
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	var flagErr error
	fs.Visit(func(f *flag.Flag) {
		if !slices.Contains(validFlags, f.Name) {
			flagErr = fmt.Errorf("-%s is not valid with %s", f.Name, cmd)
		}
		if (f.Name == "qps" && *qps <= 0) || (f.Name == "duration" && *duration <= 0) || (f.Name == "concurrency" && *concurrency <= 0) {
			flagErr = fmt.Errorf("-%s must be positive", f.Name)
		}
	})
	if flagErr != nil {
		return flagErr
	}
	if cmd == "metadata" {
		return captureMetadata(metadataOpts)
	}
	if cmd == "up" || cmd == "down" {
		return localStack(cmd, *volumes)
	}
	if cmd == "init" {
		b, _ := json.MarshalIndent(defaultConfig(), "", "  ")
		f, err := os.OpenFile(*configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(b)
		return err
	}
	if cmd == "report" {
		return report(*inputs, *labels)
	}
	if _, err := workflowIndex(*workflow); err != nil {
		return err
	}
	workflowDir := filepath.Join(*dir, *workflow)
	if cmd == "drop" {
		return dropWorkflow(workflowDir, *workflow, *address)
	}
	if cmd == "setup" || cmd == "reset" {
		return setupWorkflow(*configPath, workflowDir, *workflow, *address, *replace || cmd == "reset")
	}
	metadataBytes, err := readServerMetadata(*serverMetadataPath)
	if err != nil {
		return fmt.Errorf("server metadata: %w", err)
	}
	if metadataBytes == nil {
		fmt.Fprintln(os.Stderr, "warning: no server metadata attached; use -server-metadata PATH for hardware comparisons")
	}
	d, m, err := loadDataset(workflowDir)
	if err != nil {
		return err
	}
	if d.Config.Workflow != *workflow {
		return fmt.Errorf("dataset workflow mismatch: %s", d.Config.Workflow)
	}
	if *address != "" {
		d.Config.Address = *address
	}
	if *qps > 0 {
		d.Config.QPS = *qps
	}
	if *duration > 0 {
		d.Config.DurationSeconds = *duration
	}
	if *concurrency > 0 {
		d.Config.Concurrency = *concurrency
	}
	if err = d.Config.validate(); err != nil {
		return err
	}
	selected, err := selectedCases(d.Config, *caseName)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cli, err := connect(ctx, d.Config)
	if err != nil {
		return err
	}
	defer cli.Close(ctx)
	if err = validate(ctx, cli, d, m, selected); err != nil {
		return err
	}
	runsDir := filepath.Join(workflowDir, "runs")
	if err = os.MkdirAll(runsDir, 0755); err != nil {
		return err
	}
	runDir := filepath.Join(runsDir, time.Now().UTC().Format("20060102T150405Z"))
	if err = os.Mkdir(runDir, 0755); err != nil {
		return err
	}
	manifestBytes, err := os.ReadFile(filepath.Join(workflowDir, "manifest.json"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(runDir, "manifest.json"), manifestBytes, 0644); err != nil {
		return err
	}
	fmt.Printf("writing results to %s\n", runDir)
	if metadataBytes != nil {
		if err = os.WriteFile(filepath.Join(runDir, "server-metadata.json"), metadataBytes, 0600); err != nil {
			return err
		}
	}
	return runAll(ctx, cli, d, m, runDir, selected)
}

func setupWorkflow(configPath, dir, workflow, address string, replace bool) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	c.Workflow = workflow
	if address != "" {
		c.Address = address
	}
	var d Dataset
	var m Manifest
	_, statErr := os.Stat(filepath.Join(dir, "manifest.json"))
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	reuse := statErr == nil && !replace
	if reuse {
		d, m, err = loadDataset(dir)
		if err != nil {
			return err
		}
		saved := m.Config
		saved.Address = c.Address
		if !reflect.DeepEqual(saved, c) {
			return fmt.Errorf("existing %s dataset uses different settings; use -replace to rebuild", workflow)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	cli, err := connect(ctx, c)
	if err != nil {
		return err
	}
	defer cli.Close(ctx)
	exists, err := cli.HasCollection(ctx, milvusclient.NewHasCollectionOption(collection(c)))
	if err != nil {
		return err
	}
	if exists && !reuse && !replace {
		return fmt.Errorf("%s exists without a reusable bundle; use reset -workflow %s to rebuild", collection(c), workflow)
	}
	if !reuse {
		fmt.Printf("generating %s: %d rows\n", workflow, c.Rows)
		d = generate(c)
		m, err = saveDataset(dir, d)
		if err != nil {
			return err
		}
		fmt.Printf("generated %s: %d rows, sha256 %s\n", workflow, c.Rows, m.DataSHA256)
	} else {
		fmt.Printf("reusing %s dataset, sha256 %s\n", workflow, m.DataSHA256)
	}
	d.Config.Address = c.Address
	name := collection(c)
	if reuse && exists {
		if err = validate(ctx, cli, d, m, cases(c)); err != nil {
			return fmt.Errorf("existing %s collection failed validation; use -replace to rebuild: %w", name, err)
		}
		fmt.Printf("validated existing %s\n", workflow)
		return nil
	}
	if err = prepare(ctx, cli, d, replace); err != nil {
		return err
	}
	if err = validate(ctx, cli, d, m, cases(c)); err != nil {
		return err
	}
	fmt.Printf("validated %s\n", workflow)
	return nil
}
