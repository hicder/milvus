# Milvus CPU comparison benchmark

## Quick start

Run these from the repository root. Ensure Docker is running:

```sh
# start up etcd, minio, and milvus (via docker compose)
# other ways to start milvus are described below
go run ./tests/benchmark/cpu_comparison up

# create the collection, import data, build indexes
go run ./tests/benchmark/cpu_comparison setup -workflow hnsw

# run the load test (can rerun as you like)
go run ./tests/benchmark/cpu_comparison run -workflow hnsw -case hnsw512_not_deleted -duration 60 -qps 1000 -concurrency 64

# If you modify the config/need to rebuild the collection use reset
go run ./tests/benchmark/cpu_comparison reset -workflow hnsw
go run ./tests/benchmark/cpu_comparison run -workflow hnsw

# Stop and remove the local containers/network; keep server data for next time:
go run ./tests/benchmark/cpu_comparison down

# Or finish with a clean server: also delete this stack's Docker volumes:
go run ./tests/benchmark/cpu_comparison down -volumes
```

Choose a workflow for `setup`/`reset`/`run`, and optionally one case for `run` (omit `-case` to run all cases in that workflow):

| `-workflow` | `-case` choices |
| --- | --- |
| `hnsw` | `hnsw512_not_deleted`, `hnsw512_subreddit`, `hnsw512_recent`, `hnsw512_not_deleted_recent` |
| `prq` | `prq384_not_deleted`, `prq384_subreddit`, `prq384_recent`, `prq384_not_deleted_recent` |
| `ivf` | `ivf_flights`, `ivf_flights_not_deleted` |

For a large selectivity contrast on the same HNSW collection, run `hnsw512_not_deleted` (95% with the default config) and `hnsw512_subreddit` (1%).

Run overrides: `-duration` (seconds, default 60), `-qps` (queries per second, default 1000), and `-concurrency` (maximum in-flight requests, default 32). Edit `config.json` before setup/reset for dataset size (`rows`, default 200000), filter selectivity, index/search settings, and `warmup_seconds` (default 10, additional to measured duration). Use `-address host:19530` for another Milvus server or `-data PATH` for another bundle/results directory.

`config.json` settings: connection (`address`), collection name (`collection_prefix`), synthetic data and filter distributions (`rows` through `flight_ids`), index construction (`nlist` through `prq_nbits`), search (`queries`, `nprobe`, `ef`, `top_k`), then load generation (`qps` through `timeout_seconds`). `queries` is the size of the held-out query pool, replayed evenly; each request searches one vector. `batch_rows` controls setup inserts. Select workflows/cases with CLI flags. The file is strict JSON without comments.

`setup` reads `config.json` (or `-config PATH`) and saves its settings in the bundle. `run` uses those saved settings, with the documented CLI overrides; editing `config.json` alone does not change a run. Use `reset` to apply changed config settings. The recent filter uses a fixed seven-day cutoff relative to `anchor_unix`, within 14 generated age buckets. `subreddit_fraction` and `flight_fraction` control surviving rows, rounded down to a whole row; use `0.001` for 0.1%. Generated filter counts are checked against Milvus before running.

`up` starts Milvus, etcd, and MinIO and waits up to 180 seconds for Milvus health. `reset` regenerates the selected workflow's dataset and rebuilds its collection. To rerun against unchanged data, just use `run` again. To clear all server collections, use `down -volumes`. Deleting volumes is permanent and affects every collection in this local stack. Local dataset bundles and run results are preserved by both forms of `down`.

`down -volumes` verifies that all three benchmark volumes disappeared. If Docker reports a volume still in use, inspect its holders with `docker ps -a --filter volume=VOLUME`; exited builder containers can retain mounts. Remove only containers you no longer need, then rerun `down -volumes`.

`setup` reuses an existing compatible bundle and validates an existing collection. An incomplete collection needs `reset` (`setup -replace` is equivalent). Prior run results are preserved. `drop -workflow hnsw` removes just that collection using its saved manifest; it keeps the local bundle. To create a separate config, use `init -config PATH` (it refuses to overwrite a file).

## Results and comparison

Bundles live under `tests/benchmark/cpu_comparison/benchmark-data/WORKFLOW/`. Copy the same bundle to each machine, then use `-address` to target that machine's Milvus. Runs write JSON plus a manifest copy under `runs/YYYYMMDDTHHMMSSZ/`; a same-second collision fails rather than overwriting results.

The runner announces the start of warmup and measurement. `qps` is the offered arrival rate. An arrival is dropped when all in-flight slots are occupied or the run ends before it can be dispatched. Failed requests reached the client search call but errored or returned no rows; failures cause a nonzero exit after saving measured results. Achieved QPS is successful requests divided by elapsed time, including draining in-flight requests. p50, p95, p99, and p99.9 latencies include successful requests only; p99.9 shows `n/a` when none succeeded. Older result files without p99.9 also show `n/a` in `report`. Ctrl-C saves the interrupted measurement when one is in progress.

Long setup and run phases print a brief status about every 30 seconds; short phases stay quiet. Insert progress is reported by rows after a batch completes. These messages do not affect the result JSON.

`recall_at_k` compares one fixed query with exact L2 results; it is a sanity check, not a full recall evaluation. `goos`, `goarch`, and `cpus` describe the **load generator**, not the server. For CPU comparisons, use a separate load generator and repeat runs; profiling adds overhead.

```sh
go run ./tests/benchmark/cpu_comparison report -inputs AMD_RUN_DIR,GRAVITON_RUN_DIR -labels m7a.4xlarge,m7g.4xlarge
```

`-labels` names the server hardware in the same order as `-inputs`; use the actual instance types. Attach each server's metadata when running (below), then check the `server-metadata.json` files alongside the report. `report` requires the same dataset checksum, case set, server version, and run/search settings (including concurrency, warmup, and timeout). Connection address and collection prefix may differ.

### Server metadata

Run `metadata` from the repo on the **Linux Milvus host**, outside containers. For Docker, use that host's local daemon/context (not Docker Desktop or a remote daemon):

```sh
# Find the container ID with: docker compose -f tests/benchmark/cpu_comparison/docker-compose.yml ps -q standalone
go run ./tests/benchmark/cpu_comparison metadata -output /tmp/amd-server.json -container CONTAINER_ID -instance-type m7a.4xlarge -notes 'baseline'

# Copy the file to the load generator if separate, then attach it to each run:
go run ./tests/benchmark/cpu_comparison run -workflow hnsw -case hnsw512_not_deleted -server-metadata /tmp/amd-server.json
```

Capture includes CPU/topology (`lscpu`), kernel, memory, NUMA information when `numactl` is installed, checkout commit/status, and selected Docker image ID/digest/architecture and CPU/memory limits. Docker's zero/default limits do not mean zero available resources. Missing probes are recorded as errors; blank operator-supplied fields mean unknown. No Docker environment variables are collected.

For native Milvus, omit `-container` and record process affinity/resource limits and binary provenance in `-notes`. Checkout provenance is **not proof of the running binary's commit**, especially for published images or stale source builds. Add `-build-flags '...'` when known. Optionally use `-server-config /path/to/sanitized-milvus.yaml`: this embeds the file **verbatim**, so remove credentials first and include relevant overrides. It does not fetch effective runtime configuration or environment overrides automatically. Review the metadata before sharing.

Recapture after changes to the server/build/config; `-output` refuses to overwrite an existing file. `run` copies the supplied snapshot to `server-metadata.json` in its result directory; it does not verify that snapshot belongs to the target server or refresh it. Runs without metadata still work with a warning. `report` leaves metadata comparison to the reader: hardware/tuning are intentionally allowed to differ.

### Workload starting points

These are **untested sizing guesses**, not capacity claims. For example, [m7a.4xlarge](https://aws.amazon.com/ec2/instance-types/m7a/) and [m7g.4xlarge](https://aws.amazon.com/ec2/instance-types/general-purpose/) each provide 16 vCPUs and 64 GiB; start HNSW512 at **1 million rows**, then try **5 million** if memory permits. On a **32-vCPU / 128-GiB** server, start at **5 million**, then try **10 million**. vCPU counts alone do not establish equivalent CPU topology.

Use `queries=1000`, `warmup_seconds=30`, `-duration 120`, and start `-concurrency 64`. Sweep `-qps 100`, `500`, `1000`, then higher as appropriate, comparing the default 95% and 1% HNSW cases. Repeat each point three times; watch server CPU, memory, p95, failures, and drops. Check load-generator CPU and concurrency headroom before interpreting dropped requests as server saturation. These are suggestions, not changed defaults; edit the config and `reset` when changing rows/query-pool/warmup.

HNSW512 raw vectors alone use about **2 GB per million rows**; indexes, build-time working memory, Milvus overhead, and the generator's in-memory dataset require additional headroom. Avoid swapping, especially if generator and server share a host. Start smaller if setup is memory-constrained; the default 100k rows remains a smoke test.

## Start Milvus

A couple options to start Milvus:

### Docker standalone image

```sh
# docker image
go run ./tests/benchmark/cpu_comparison up
```

### Native Linux source build

Follow the repository's [native build prerequisites](../../../DEVELOPMENT.md#building-milvus-on-a-local-osshell-environment) first. Choose only one launch route at a time, since each publishes port 19530.

For a native Linux source build, start the benchmark dependencies, then build and run Milvus:

```sh
docker compose -f tests/benchmark/cpu_comparison/docker-compose.yml up -d etcd minio
make milvus
ETCD_ENDPOINTS=127.0.0.1:2379 MINIO_ADDRESS=127.0.0.1:9000 LD_LIBRARY_PATH="./internal/core/output/lib:./lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ./bin/milvus run standalone
```

### Development builder container

For the repository's development builder container, build and run Milvus inside the Linux builder. The root Compose file supplies its dependencies; the small override publishes the Milvus ports:

```sh
docker compose -f docker-compose.yml -f tests/benchmark/cpu_comparison/builder-compose.yml run --service-ports builder bash -lc 'make milvus && LD_LIBRARY_PATH="./internal/core/output/lib:./lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ./bin/milvus run standalone'
```

See the [builder guide](../../../build/README.md). The root `.env` defaults to `IMAGE_ARCH=amd64`; choose the architecture of the Linux host. `up`/`down` manage only the benchmark Docker standalone stack. Stop a foreground source/builder process with Ctrl-C. For native source dependencies, use `down` afterward. To stop the builder's separate Compose stack, use `docker compose -f docker-compose.yml -f tests/benchmark/cpu_comparison/builder-compose.yml down --remove-orphans`.

## TODO

- [ ] review production Milvus scalar indexes, search parameters, segment sizes, thread pools, mmap, consistency, ignore-growing, partition selection, and group-by settings. Adapt relevant settings for a local single-node CPU benchmark; production settings target distributed Kubernetes deployments.
