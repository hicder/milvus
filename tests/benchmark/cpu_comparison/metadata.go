package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Only explicitly selected fields are collected: never dump Docker environment
// variables, process environments, or an entire Docker inspect response.
const containerMetadataFormat = `{"id":{{json .Id}},"image_id":{{json .Image}},"image_reference":{{json .Config.Image}},"running":{{json .State.Running}},"nano_cpus":{{json .HostConfig.NanoCpus}},"cpu_quota":{{json .HostConfig.CpuQuota}},"cpu_period":{{json .HostConfig.CpuPeriod}},"cpuset_cpus":{{json .HostConfig.CpusetCpus}},"cpuset_mems":{{json .HostConfig.CpusetMems}},"memory_bytes":{{json .HostConfig.Memory}},"memory_swap_bytes":{{json .HostConfig.MemorySwap}}}`
const imageMetadataFormat = `{"id":{{json .Id}},"repo_digests":{{json .RepoDigests}},"os":{{json .Os}},"architecture":{{json .Architecture}}}`

type metadataOptions struct {
	Output, Container, ServerConfig, InstanceType, BuildFlags, Notes string
}

type metadataProbe struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type serverMetadata struct {
	SchemaVersion int                      `json:"schema_version"`
	CapturedAt    time.Time                `json:"captured_at"`
	OS            string                   `json:"os"`
	InstanceType  string                   `json:"instance_type"`
	BuildFlags    string                   `json:"build_flags_user_supplied"`
	Notes         string                   `json:"notes"`
	ServerConfig  string                   `json:"server_config_user_supplied,omitempty"`
	Probes        map[string]metadataProbe `json:"probes"`
}

type metadataCommand func(context.Context, string, ...string) ([]byte, error)

func collectMetadata(ctx context.Context, opts metadataOptions, run metadataCommand) (serverMetadata, error) {
	m := serverMetadata{SchemaVersion: 1, CapturedAt: time.Now().UTC(), OS: "linux",
		InstanceType: opts.InstanceType, BuildFlags: opts.BuildFlags, Notes: opts.Notes,
		Probes: map[string]metadataProbe{}}
	if opts.ServerConfig != "" {
		b, err := os.ReadFile(opts.ServerConfig)
		if err != nil {
			return m, fmt.Errorf("read sanitized server config: %w", err)
		}
		m.ServerConfig = string(b)
	}
	probe := func(key, command string, args ...string) string {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		b, err := run(probeCtx, command, args...)
		p := metadataProbe{Output: strings.TrimSpace(string(b))}
		if err != nil {
			p.Error = err.Error()
		}
		m.Probes[key] = p
		return p.Output
	}
	probe("kernel", "uname", "-srmo")
	probe("cpu_topology", "lscpu")
	probe("memory", "cat", "/proc/meminfo")
	probe("numa", "numactl", "--hardware")
	// This identifies the checkout, not necessarily the running binary.
	probe("checkout_commit", "git", "rev-parse", "HEAD")
	probe("checkout_status", "git", "status", "--porcelain")
	if opts.Container != "" {
		out := probe("container", "docker", "container", "inspect", "--format", containerMetadataFormat, opts.Container)
		var container struct {
			ImageID string `json:"image_id"`
		}
		if json.Unmarshal([]byte(out), &container) == nil && container.ImageID != "" {
			probe("image", "docker", "image", "inspect", "--format", imageMetadataFormat, container.ImageID)
		}
	}
	return m, nil
}

func captureMetadata(opts metadataOptions) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("metadata must run on the Linux Milvus server host, not the load generator or a Mac")
	}
	if opts.Output == "" {
		return fmt.Errorf("metadata requires -output PATH")
	}
	m, err := collectMetadata(context.Background(), opts, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		return cmd.Output()
	})
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(opts.Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for name, p := range m.Probes {
		if p.Error != "" {
			fmt.Fprintf(os.Stderr, "metadata: %s unavailable: %s\n", name, p.Error)
		}
	}
	fmt.Printf("saved server metadata to %s; review before sharing (missing fields are not inferred)\n", opts.Output)
	return nil
}

func readServerMetadata(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m serverMetadata
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.SchemaVersion != 1 || m.OS != "linux" || m.CapturedAt.IsZero() || len(m.Probes) == 0 {
		return nil, fmt.Errorf("invalid server metadata: use metadata on the Linux server host")
	}
	return b, nil
}
