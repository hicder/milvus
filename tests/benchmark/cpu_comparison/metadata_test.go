package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestMetadataCaptureAndRead(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "sanitized.yaml")
	if err := os.WriteFile(config, []byte("queryNode:\n  knowhereThreadPoolNumRatio: 4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var commands []string
	m, err := collectMetadata(context.Background(), metadataOptions{Container: "milvus", ServerConfig: config, InstanceType: "example", BuildFlags: "release"}, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("probe has no timeout")
		}
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "numactl" {
			return nil, errors.New("not installed")
		}
		if name == "docker" && args[0] == "container" {
			return []byte(`{"image_id":"sha256:example"}`), nil
		}
		return []byte("captured\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Probes["numa"].Error == "" || m.Probes["kernel"].Output != "captured" || m.ServerConfig == "" || m.BuildFlags != "release" {
		t.Fatalf("incomplete metadata: %+v", m)
	}
	if !strings.HasSuffix(commands[len(commands)-1], "sha256:example") {
		t.Fatal("did not inspect exact running image")
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "server-metadata.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readServerMetadata(path)
	if err != nil || !bytes.Equal(b, got) {
		t.Fatalf("metadata round trip: %s, %v", got, err)
	}
	if _, err := readServerMetadata(config); err == nil {
		t.Fatal("accepted non-metadata file")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readServerMetadata(path); err == nil {
		t.Fatal("accepted missing metadata identity")
	}
	if b, err := readServerMetadata(""); b != nil || err != nil {
		t.Fatal("optional metadata failed")
	}
}

func TestMetadataNativeAndMissingConfig(t *testing.T) {
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "docker" {
			t.Fatal("native capture invoked Docker")
		}
		return []byte("captured"), nil
	}
	m, err := collectMetadata(context.Background(), metadataOptions{}, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Probes["container"]; ok {
		t.Fatal("native capture invented container metadata")
	}
	if _, err := collectMetadata(context.Background(), metadataOptions{ServerConfig: filepath.Join(t.TempDir(), "missing")}, run); err == nil {
		t.Fatal("silently omitted requested configuration")
	}
}

func TestContainerMetadataAllowlist(t *testing.T) {
	var input any
	if err := json.Unmarshal([]byte(`{"Id":"container-id","Image":"sha256:image","Config":{"Image":"milvus:tag","Env":["SECRET=do-not-share"]},"State":{"Running":true},"HostConfig":{"NanoCpus":2000000000,"CpuQuota":0,"CpuPeriod":0,"CpusetCpus":"0-1","CpusetMems":"0","Memory":4096,"MemorySwap":8192}}`), &input); err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("inspect").Funcs(template.FuncMap{"json": func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }}).Parse(containerMetadataFormat)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, input); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b.Bytes()) || strings.Contains(b.String(), "SECRET") || !strings.Contains(b.String(), `"memory_bytes":4096`) {
		t.Fatalf("invalid/unsafe inspect output: %s", b.String())
	}
}

func TestReportWithServerMetadata(t *testing.T) {
	var dirs []string
	for range 2 {
		dir := t.TempDir()
		dirs = append(dirs, dir)
		for name, value := range map[string]any{
			"manifest.json":        Manifest{DataSHA256: "same"},
			"case.json":            RunResult{Case: "case", DatasetSHA256: "same", Config: defaultConfig(), ServerVersion: "2.6.24"},
			"server-metadata.json": map[string]string{"notes": "metadata is not a case result"},
		} {
			b, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := report(strings.Join(dirs, ","), "AMD,Graviton"); err != nil {
		t.Fatal(err)
	}
}
