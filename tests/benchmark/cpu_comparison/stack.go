package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

func localStack(action string, volumes bool) error {
	args := []string{"compose", "-f", "tests/benchmark/cpu_comparison/docker-compose.yml"}
	switch action {
	case "up":
		args = append(args, "up", "-d", "--wait", "--wait-timeout", "180")
	case "down":
		args = append(args, "down")
		if volumes {
			args = append(args, "--volumes")
		}
	default:
		return fmt.Errorf("unknown stack action %q", action)
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local Docker stack %s: %w", action, err)
	}
	if action == "down" && volumes {
		remaining, err := remainingStackVolumes(args[:3])
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return fmt.Errorf("Docker left benchmark volumes in use: %s; inspect holders with docker ps -a --filter volume=VOLUME, then remove those containers and rerun down -volumes", strings.Join(remaining, ", "))
		}
		fmt.Println("removed local Docker stack and its volumes; generated datasets and run results are preserved")
	}
	return nil
}

func remainingStackVolumes(composeArgs []string) ([]string, error) {
	config, err := exec.Command("docker", append(slices.Clone(composeArgs), "config", "--format", "json")...).Output()
	if err != nil {
		return nil, fmt.Errorf("read benchmark Compose volume names: %w", err)
	}
	var spec struct {
		Volumes map[string]struct {
			Name string `json:"name"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(config, &spec); err != nil {
		return nil, fmt.Errorf("parse benchmark Compose volumes: %w", err)
	}
	if len(spec.Volumes) == 0 {
		return nil, fmt.Errorf("benchmark Compose config has no named volumes to verify")
	}
	output, err := exec.Command("docker", "volume", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list Docker volumes after down: %w", err)
	}
	existing := strings.Fields(string(output))
	var remaining []string
	for _, v := range spec.Volumes {
		if v.Name == "" {
			return nil, fmt.Errorf("benchmark Compose volume has no resolved name")
		}
		if slices.Contains(existing, v.Name) {
			remaining = append(remaining, v.Name)
		}
	}
	slices.Sort(remaining)
	return remaining, nil
}
