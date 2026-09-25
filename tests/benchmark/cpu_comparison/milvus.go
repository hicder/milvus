package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

func connect(ctx context.Context, c Config) (*milvusclient.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cli, err := milvusclient.New(dialCtx, &milvusclient.ClientConfig{Address: c.Address})
	if err != nil {
		return nil, fmt.Errorf("connect to Milvus at %s: %w; start the selected Milvus server before setup", c.Address, err)
	}
	v, err := cli.GetServerVersion(dialCtx, milvusclient.NewGetServerVersionOption())
	if err != nil {
		cli.Close(ctx)
		return nil, fmt.Errorf("read Milvus version at %s: %w; start the selected Milvus server before setup", c.Address, err)
	}
	if strings.TrimPrefix(v, "v") != "2.6.24" {
		cli.Close(ctx)
		return nil, fmt.Errorf("server version %q, need 2.6.24", v)
	}
	return cli, nil
}
func collection(c Config) string {
	return c.CollectionPrefix + "_" + strings.ReplaceAll(c.Workflow, "-", "_")
}
func dimension(idx string) int {
	switch idx {
	case "hnsw512":
		return 512
	case "prq384":
		return 384
	default:
		return 128
	}
}
func vectorIndex(c Config, idx string) index.Index {
	switch idx {
	case "hnsw512":
		return index.NewHNSWIndex(entity.L2, c.HNSWM, c.EfConstruction)
	case "prq384":
		return index.NewGenericIndex("", map[string]string{"index_type": "HNSW_PRQ", "metric_type": "L2", "M": strconv.Itoa(c.HNSWM), "efConstruction": strconv.Itoa(c.EfConstruction), "nrq": strconv.Itoa(c.PRQNRQ), "m": strconv.Itoa(c.PRQM), "nbits": strconv.Itoa(c.PRQNBits)})
	default:
		return index.NewIvfFlatIndex(entity.L2, c.NList)
	}
}
func prepare(ctx context.Context, cli *milvusclient.Client, d Dataset, replace bool) error {
	c := d.Config
	idx, err := workflowIndex(c.Workflow)
	if err != nil {
		return err
	}
	name := collection(c)
	fmt.Printf("preparing %s (%d rows)\n", name, len(d.Rows))
	exists, err := cli.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if err != nil {
		return err
	}
	if exists {
		if !replace {
			return fmt.Errorf("%s exists; use --replace to rebuild only this benchmark collection", name)
		}
		if err = cli.DropCollection(ctx, milvusclient.NewDropCollectionOption(name)); err != nil {
			return err
		}
	}
	schema := entity.NewSchema().WithDynamicFieldEnabled(false).
		WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeInt64).WithIsPrimaryKey(true)).
		WithField(entity.NewField().WithName("embedding").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(dimension(idx))))
	type indexSpec struct {
		field string
		idx   index.Index
	}
	specs := []indexSpec{{"embedding", vectorIndex(c, idx)}}
	switch c.Workflow {
	case "hnsw", "prq":
		schema.WithField(entity.NewField().WithName("is_deleted").WithDataType(entity.FieldTypeBool)).WithField(entity.NewField().WithName("created_time").WithDataType(entity.FieldTypeInt64)).WithField(entity.NewField().WithName("subreddit_id").WithDataType(entity.FieldTypeInt64))
		specs = append(specs, indexSpec{"is_deleted", index.NewBitmapIndex()}, indexSpec{"created_time", index.NewInvertedIndex()})
		specs = append(specs, indexSpec{"subreddit_id", index.NewInvertedIndex()})
	case "ivf":
		schema.WithField(entity.NewField().WithName("flight_id").WithDataType(entity.FieldTypeInt64)).WithField(entity.NewField().WithName("is_deleted").WithDataType(entity.FieldTypeBool))
		specs = append(specs, indexSpec{"flight_id", index.NewInvertedIndex()}, indexSpec{"is_deleted", index.NewBitmapIndex()})
	}
	if err = cli.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(name, schema).WithShardNum(1)); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	nextProgress := time.Now().Add(progressInterval)
	for start := 0; start < len(d.Rows); start += c.BatchRows {
		end := min(start+c.BatchRows, len(d.Rows))
		n := end - start
		ids := make([]int64, n)
		deleted := make([]bool, n)
		created := make([]int64, n)
		sub := make([]int64, n)
		flight := make([]int64, n)
		vectors := make([][]float32, n)
		for i, row := range d.Rows[start:end] {
			ids[i] = row.ID
			deleted[i] = row.Deleted
			created[i] = row.Created
			sub[i] = row.Subreddit
			flight[i] = row.Flight
			vectors[i] = rowVector(row, idx)
		}
		opt := milvusclient.NewColumnBasedInsertOption(name).WithInt64Column("id", ids).WithFloatVectorColumn("embedding", dimension(idx), vectors)
		switch c.Workflow {
		case "hnsw", "prq":
			opt.WithBoolColumn("is_deleted", deleted).WithInt64Column("created_time", created).WithInt64Column("subreddit_id", sub)
		case "ivf":
			opt.WithInt64Column("flight_id", flight).WithBoolColumn("is_deleted", deleted)
		}
		_, err = cli.Insert(ctx, opt)
		if err != nil {
			return fmt.Errorf("insert %s at %d: %w", name, start, err)
		}
		if time.Now().After(nextProgress) {
			fmt.Printf("%s: inserted %d/%d rows\n", name, end, len(d.Rows))
			nextProgress = time.Now().Add(progressInterval)
		}
	}
	fmt.Printf("%s: inserted %d rows; flushing\n", name, len(d.Rows))
	flush, err := cli.Flush(ctx, milvusclient.NewFlushOption(name))
	if err != nil {
		return err
	}
	if err = awaitProgress(ctx, "flushing "+name, flush.Await); err != nil {
		return err
	}
	for _, spec := range specs {
		fmt.Printf("building %s.%s index\n", name, spec.field)
		task, e := cli.CreateIndex(ctx, milvusclient.NewCreateIndexOption(name, spec.field, spec.idx).WithIndexName(spec.field+"_idx"))
		if e != nil {
			return fmt.Errorf("index %s.%s: %w", name, spec.field, e)
		}
		if e = awaitProgress(ctx, "building "+name+"."+spec.field, task.Await); e != nil {
			return e
		}
	}
	fmt.Printf("loading %s\n", name)
	load, err := cli.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		return err
	}
	if err = awaitProgress(ctx, "loading "+name, load.Await); err != nil {
		return err
	}
	fmt.Printf("loaded %s\n", name)
	return nil
}
func queryVector(d Dataset, idx string, n int) []float32 {
	switch idx {
	case "hnsw512":
		return d.Q512[n%len(d.Q512)]
	case "prq384":
		return d.Q384[n%len(d.Q384)]
	default:
		return d.Q128[n%len(d.Q128)]
	}
}
func search(ctx context.Context, cli *milvusclient.Client, d Dataset, cs Case, n int) ([]milvusclient.ResultSet, error) {
	c := d.Config
	opt := milvusclient.NewSearchOption(collection(c), c.TopK, []entity.Vector{entity.FloatVector(queryVector(d, cs.Index, n))}).WithANNSField("embedding").WithFilter(cs.Filter).WithOutputFields("id").WithConsistencyLevel(entity.ClStrong)
	if cs.Index == "ivf128" {
		opt.WithAnnParam(index.NewIvfAnnParam(c.NProbe))
	} else {
		opt.WithAnnParam(index.NewHNSWAnnParam(c.Ef))
	}
	return cli.Search(ctx, opt)
}
func validate(ctx context.Context, cli *milvusclient.Client, d Dataset, m Manifest, selected []Case) error {
	stop := progress("validating " + d.Config.Workflow)
	defer stop()
	idx, err := workflowIndex(d.Config.Workflow)
	if err != nil {
		return err
	}
	description, err := cli.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(collection(d.Config), "embedding_idx"))
	if err != nil {
		return err
	}
	if description.Index == nil {
		return fmt.Errorf("embedding_idx is missing")
	}
	for key, want := range vectorIndex(d.Config, idx).Params() {
		if got := description.Params()[key]; got != want {
			return fmt.Errorf("embedding_idx %s=%q, expected %q; reset the workflow", key, got, want)
		}
	}
	stats, err := cli.GetCollectionStats(ctx, milvusclient.NewGetCollectionStatsOption(collection(d.Config)))
	if err != nil {
		return err
	}
	if stats["row_count"] != strconv.Itoa(len(d.Rows)) {
		return fmt.Errorf("row count %q, expected %d", stats["row_count"], len(d.Rows))
	}
	lookup := make(map[int64]Row, len(d.Rows))
	for _, r := range d.Rows {
		lookup[r.ID] = r
	}
	for _, cs := range selected {
		count := 0
		for _, row := range d.Rows {
			if match(row, cs, d.Config) {
				count++
			}
		}
		if count != m.Counts[cs.Name] {
			return fmt.Errorf("manifest count mismatch for %s", cs.Name)
		}
		if count == 0 {
			return fmt.Errorf("%s has no eligible rows; increase rows or filter survival fraction", cs.Name)
		}
		result, err := cli.Query(ctx, milvusclient.NewQueryOption(collection(d.Config)).WithFilter(cs.Filter).WithOutputFields("count(*)").WithConsistencyLevel(entity.ClStrong))
		if err != nil {
			return fmt.Errorf("%s filter count: %w", cs.Name, err)
		}
		if len(result.Fields) != 1 {
			return fmt.Errorf("%s: invalid count response", cs.Name)
		}
		actual, err := result.Fields[0].GetAsInt64(0)
		if err != nil {
			return err
		}
		if actual != int64(count) {
			return fmt.Errorf("%s matches %d server rows, expected %d", cs.Name, actual, count)
		}
		fmt.Printf("%s: %d/%d rows match (%.2f%%)\n", cs.Name, count, len(d.Rows), 100*float64(count)/float64(len(d.Rows)))
		for q := 0; q < min(3, d.Config.Queries); q++ {
			sets, err := search(ctx, cli, d, cs, q)
			if err != nil {
				return fmt.Errorf("%s: %w", cs.Name, err)
			}
			if err = searchResultError(sets); err != nil {
				return fmt.Errorf("%s: %w", cs.Name, err)
			}
			for i := 0; i < sets[0].ResultCount; i++ {
				id, err := sets[0].IDs.GetAsInt64(i)
				if err != nil {
					return err
				}
				row, ok := lookup[id]
				if !ok || !match(row, cs, d.Config) {
					return fmt.Errorf("%s returned ineligible id %d", cs.Name, id)
				}
			}
		}
	}
	return nil
}

func exactRecall(ctx context.Context, cli *milvusclient.Client, d Dataset, cs Case) (float64, error) {
	if len(d.Rows) == 0 {
		return 0, fmt.Errorf("empty dataset")
	}
	query := queryVector(d, cs.Index, 0)
	type neighbor struct {
		id       int64
		distance float64
	}
	neighbors := make([]neighbor, 0, len(d.Rows))
	for _, row := range d.Rows {
		if !match(row, cs, d.Config) {
			continue
		}
		v := rowVector(row, cs.Index)
		distance := 0.0
		for i, x := range query {
			delta := float64(x) - float64(v[i])
			distance += delta * delta
		}
		neighbors = append(neighbors, neighbor{row.ID, distance})
	}
	if len(neighbors) == 0 {
		return 0, fmt.Errorf("%s has no eligible rows", cs.Name)
	}
	sort.Slice(neighbors, func(i, j int) bool { return neighbors[i].distance < neighbors[j].distance })
	k := min(d.Config.TopK, len(neighbors))
	expected := make(map[int64]bool, k)
	for _, n := range neighbors[:k] {
		expected[n.id] = true
	}
	sets, err := search(ctx, cli, d, cs, 0)
	if err != nil {
		return 0, err
	}
	if err = searchResultError(sets); err != nil {
		return 0, fmt.Errorf("%s recall: %w", cs.Name, err)
	}
	hits := 0
	for i := 0; i < sets[0].ResultCount; i++ {
		id, err := sets[0].IDs.GetAsInt64(i)
		if err != nil {
			return 0, err
		}
		if expected[id] {
			hits++
		}
	}
	return float64(hits) / float64(k), nil
}

func searchResultError(sets []milvusclient.ResultSet) error {
	if len(sets) != 1 {
		return fmt.Errorf("expected one search result set, got %d", len(sets))
	}
	if sets[0].Err != nil {
		return sets[0].Err
	}
	if sets[0].ResultCount == 0 {
		return fmt.Errorf("search returned no rows for a nonempty filter")
	}
	return nil
}
