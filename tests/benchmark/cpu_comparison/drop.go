package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

func dropWorkflow(dir, workflow, address string) error {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read workflow manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("parse workflow manifest: %w", err)
	}
	if m.Config.Workflow != workflow || m.Config.CollectionPrefix == "" {
		return fmt.Errorf("manifest does not identify workflow %q and a collection prefix", workflow)
	}
	_, err = workflowIndex(workflow)
	if err != nil {
		return err
	}
	c := m.Config
	if address != "" {
		c.Address = address
	}
	name := collection(c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli, err := connect(ctx, c)
	if err != nil {
		return err
	}
	defer cli.Close(ctx)
	exists, err := cli.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("%s does not exist; nothing to drop\n", name)
		return nil
	}
	if err := cli.DropCollection(ctx, milvusclient.NewDropCollectionOption(name)); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	exists, err = cli.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%s still exists after drop", name)
	}
	fmt.Printf("dropped %s; local dataset and run results remain in %s\n", name, dir)
	return nil
}
