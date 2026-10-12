# Selected Milvus CPU repro cases

Run these three synthetic cases to investigate filtered vector-search CPU performance. Both workflows contain one million seeded random vectors and replay 1000 query vectors. The posts cases use 512D HNSW/IP; ads uses 100D IVF_FLAT/L2. Milvus 2.6.24 is required. The data, selectivity and embeddings are synthetic.

| Case | Offered QPS | Concurrency | Purpose |
| --- | ---: | ---: | --- |
| `hnsw512_not_deleted_recent` | 3000 | 256 | IP graph search with STL_SORT timestamp range and deletion bitmap; about 47.5% survive |
| `hnsw512_subreddit` | 3000 | 256 | Restrictive membership filter; 1% survival can trigger native brute-force fallback |
| `ivf_flights_not_deleted` | 6000 | 256 | 64-flight membership list plus deletion filtering; about 0.95% survive |

Each run warms up for 30 seconds and measures for 30 seconds. Setup, index building and sample recall are outside the measured window. Run one case at a time. PRQ is available in the general benchmark but is not a priority in this handoff.

## Get and build the load generator

```sh
git clone --branch nathanwilk7/cpu-benchmarks-stl-ip --single-branch https://github.com/nathanwilk7/milvus.git
cd milvus
CGO_ENABLED=0 go build -o cpu-bench ./tests/benchmark/cpu_comparison
export GOMEMLIMIT=24GiB
```

Use the Go version required by `go.mod` or newer. The binary above is the load generator, not a Milvus source build. Run it on a separate Linux host from the server when possible. Allow 32 GiB of memory for the generator: one-million-row generation and serialization need extra memory beyond raw vector storage. `GOMEMLIMIT` is a soft Go limit, not a container memory limit. Record generator CPU usage when interpreting drops at high QPS.

For Milvus/Knowhere source builds and changes, follow the [Linux build instructions](README.md#linux-source-build-in-the-development-builder-recommended). Preserve source/build settings and use native x86-64 execution on AMD rather than ARM emulation.

## Start the posts server

Run this on the Milvus server host, from a checkout of this branch. Docker Engine and Docker Compose are required. The repro stack uses embedded etcd, local storage and RocksMQ. All server data is in its own Docker volume.

```sh
docker compose -p cpu-repro \
  -f tests/benchmark/cpu_comparison/docker-compose.repro.yml \
  -f tests/benchmark/cpu_comparison/docker-compose.posts.yml \
  up -d --wait
```

The posts server has **32 GiB memory, GOMAXPROCS=30, and no CPU quota**. Unlike a Kubernetes CPU request, this Docker profile does not reserve 30 CPUs. The ads profile below uses **15 GiB, GOMAXPROCS=22 and a 22-CPU quota**. These are example resource budgets; CPU placement depends on the host.

Both profiles configure automatic SIMD selection, read-concurrency ratio 1, Knowhere pool ratio 4, cgo pool ratio 2, and the selected mmap/filter settings. Record actual CPU model, physical/logical cores, SMT, NUMA, container limits, and selected SIMD kernels with any findings.

## Run the two posts cases

From the load-generator checkout, replace `SERVER_HOST:19530` with the benchmark server's address. Use `127.0.0.1:19530` if running the generator locally on the server for a smoke test. The config uses HNSW M=30, efConstruction=500, ef=128 and top-k 10. Boolean deletion uses BITMAP, timestamps STL_SORT, and subreddit membership INVERTED.

```sh
./cpu-bench setup -workflow hnsw \
  -config tests/benchmark/cpu_comparison/config-posts.json \
  -address SERVER_HOST:19530 -data ./cpu-repro-data

./cpu-bench run -workflow hnsw -case hnsw512_not_deleted_recent \
  -data ./cpu-repro-data -qps 3000 -duration 30 -concurrency 256

./cpu-bench run -workflow hnsw -case hnsw512_subreddit \
  -data ./cpu-repro-data -qps 3000 -duration 30 -concurrency 256
```

`setup` waits for insert, flush, index creation and load before validating the filters. First-time HNSW setup can take tens of minutes. If the server has an incomplete or older benchmark collection, use `reset` with the same workflow/config/address/data flags; that rebuilds only the corresponding benchmark collection. Editing a config after setup does not change the saved dataset or search settings used by `run`.

Start at 1000 QPS if checking basic functionality first. If tuning, change QPS at a fixed concurrency and retain failures/drops alongside latency. The listed QPS values are starting points; adjust them to exercise the desired latency range on your machine.

## Switch to the ads server and run IVF

On the server host, stop the posts profile and start the ads profile. The volume is preserved, so this does not erase the posts collection.

```sh
docker compose -p cpu-repro \
  -f tests/benchmark/cpu_comparison/docker-compose.repro.yml \
  -f tests/benchmark/cpu_comparison/docker-compose.posts.yml down

docker compose -p cpu-repro \
  -f tests/benchmark/cpu_comparison/docker-compose.repro.yml \
  -f tests/benchmark/cpu_comparison/docker-compose.ads.yml \
  up -d --wait
```

Then, on the generator:

```sh
./cpu-bench setup -workflow ivf \
  -config tests/benchmark/cpu_comparison/config-ads.json \
  -address SERVER_HOST:19530 -data ./cpu-repro-data

./cpu-bench run -workflow ivf -case ivf_flights_not_deleted \
  -data ./cpu-repro-data -qps 6000 -duration 30 -concurrency 256
```

Ads uses nlist=256, nprobe=8, top-k=1000, 64 flight IDs, eventual consistency and `ignore_growing=true`. It returns row and flight IDs. Old 128D IVF collections/bundles need rebuilding with `reset`; do not reuse their results as this profile's baseline.

If 6000 QPS overloads the machine, start at 1000 and increase. For example, tune toward the highest achieved QPS with p99 below 20 ms and no failures or drops.

## Accuracy and synthetic workload limits

The 1% flight survival setting is synthetic. The requests all use the same list; the benchmark omits real data skew, changing request eligibility, partition layouts and real embeddings. Standalone also shares resources among all Milvus roles; these budgets do not isolate QueryNode alone.

The single-query recall check is a sanity check, not a recall distribution. In particular, ads may return fewer than 1000 eligible neighbors from eight probed lists. Preserve recall/search settings in results and investigate accuracy separately before making capacity claims. These settings are useful to reproduce execution hotspots; they do not establish application-quality recall.

## Profile and send findings

The console reports achieved QPS, successful-request p50/p95/p99/p99.9, sample recall, failures and dropped arrivals. JSON results are saved under `./cpu-repro-data/WORKFLOW/runs/`. Send those summaries along with machine/container/build settings and any profiling observations.

For a flamegraph, run the selected case for 60 measured seconds, find the **host PID of `milvus run standalone`** (not tini), and start this helper when the generator prints `measuring`:

```sh
bash tests/benchmark/cpu_comparison/capture-perf.sh \
  MILVUS_HOST_PID hnsw512_not_deleted_recent 3000 fp
```

Run it on the server host with sudo/perf access. It records only that process, including its threads, for 30 seconds at 99 Hz; symbolizes while the container filesystem exists; and writes raw stacks, folded stacks, and an SVG when `~/FlameGraph` is installed. Install those tools with `git clone --depth 1 https://github.com/brendangregg/FlameGraph.git ~/FlameGraph` if needed. Record profiled runs separately from unprofiled latency runs. If native stacks are truncated, check symbols/frame pointers and consider a separate `dwarf` capture with the helper. CPU samples alone cannot attribute blocked queue latency.

The most useful findings are the main CPU/memory bottleneck, selected distance kernels, and any compiler/config/code changes that improve these cases. Keep algorithm/search settings fixed when testing hardware or compilation changes; report accuracy changes if tuning search itself.
