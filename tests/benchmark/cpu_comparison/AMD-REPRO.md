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
git clone --branch nathanwilk7/cpu-benchmarks --single-branch https://github.com/hicder/milvus.git
cd milvus
CGO_ENABLED=0 go build -o cpu-bench ./tests/benchmark/cpu_comparison
export GOMEMLIMIT=24GiB
```

Use the Go version required by `go.mod` or newer. The binary above is the load generator, not a Milvus source build. Run it on a separate Linux host from the server when possible. Allow 32 GiB of memory for the generator: one-million-row generation and serialization need extra memory beyond raw vector storage. `GOMEMLIMIT` is a soft Go limit, not a container memory limit. Record generator CPU usage when interpreting drops at high QPS.

## Build and run a modified Milvus server

The published Docker image is optional. Use the **same branch cloned above** for source experiments; skip the clone step in the general build guide. Choose either route:

- **Host-native Milvus on an AMD EC2 instance:** follow [Native Linux source build](README.md#native-linux-source-build) for toolchain installation, dependency startup, compilation and launch. Milvus runs directly on the host; that recipe uses Docker only for etcd and MinIO. It requires Go, GCC/C++, CMake, Conan 1.x and Rust; use the versions documented in the guide.
- **Source build in the development container:** follow [Linux source build in the development builder](README.md#linux-source-build-in-the-development-builder-recommended). The host checkout is mounted into the builder, so local edits are compiled into the server you launch there. This is separate from running the published Milvus image. Use `IMAGE_ARCH=amd64` on AMD/x86-64.

Once the chosen build environment is ready, the core build/rebuild command is:

```sh
make milvus MILVUS_VERSION=2.6.24 mode=Release
```

The version override is needed by this branch's benchmark version check. For profiling builds, use `mode=RelWithDebInfo`. Follow the chosen route's launch command, which sources `scripts/setenv.sh` to load the built C++ libraries before executing `./bin/milvus run standalone`.

Before launching, merge `common.simdType`, `queryNode` and `dataCoord` from [repro-posts.yaml](repro-posts.yaml) or [repro-ads.yaml](repro-ads.yaml) into the source checkout's `configs/user.yaml`. Preserve the chosen route's storage/etcd settings: the Docker repro profiles use embedded etcd/local storage, whereas the general source recipes use external etcd/MinIO. Set `GOMAXPROCS=30` for posts or `GOMAXPROCS=22` for ads **in the server's launch shell**. For the builder route, set it inside the container. `GOMAXPROCS` does not enforce a process CPU or memory limit; apply any desired resource limits separately.

Use the setup/run commands below with the source-built server's address, skipping the published-image startup commands. Stop the old server before rebuilding, then restart the newly built binary. Reuse the collection for code-only experiments; rebuild it with `reset` for schema/index changes or when a changed index-building algorithm requires a new index. Keep the server dependencies running during this loop.

For changes inside the vector-search engine, see [Modify Knowhere and other dependencies](README.md#modify-knowhere-and-other-dependencies): it shows the pinned Knowhere checkout and `FETCHCONTENT_SOURCE_DIR_KNOWHERE` override so your edits actually enter the Milvus build. See [Distance kernels, SIMD, and build options](README.md#distance-kernels-simd-and-build-options) for compile flags, runtime kernel selection and profiling settings. Record the changed flags/patch and check startup logs for the selected kernels. A successful build alone does not verify the benchmark; run a smoke test on the target host before increasing QPS.

## Reference Kubernetes execution environment

The Graviton exercise used standalone Milvus in manually launched Kubernetes pods, with a separate load-generator pod on another node. All Milvus roles shared the server pod's budget. These are the benchmark environment details, not a distributed Milvus deployment description. Runtime captures were taken on October 12, 2026; node and Kubernetes versions were verified the same day.

| Item | Reference environment |
| --- | --- |
| Server node | AWS EC2 `c8g.24xlarge`, ARM64; `lscpu` reported Neoverse-V2, 96 logical CPUs, one thread per core and one NUMA node |
| Host OS / kernel | Ubuntu 24.04.5 LTS / `7.0.0-1012-aws` |
| Kubernetes API server / node kubelet | `v1.33.12` / `v1.33.12` |
| Container runtime / cgroups | CRI-O `1.33.13` / cgroup v2 |
| Milvus | `milvusdb/milvus:v2.6.24`, launched as `milvus run standalone` |
| Storage and dependencies | Embedded etcd, local storage, RocksMQ; pod `emptyDir` for server data (40 GiB size limit) |
| Load generator | Separate node; CPU request/limit 4, memory request/limit 32 GiB, `GOMAXPROCS=4` after increasing setup memory |

| Server pod setting | Posts | Ads |
| --- | --- | --- |
| CPU request | 30 | 22 |
| CPU limit in pod specification | Unset | 22 |
| Memory request / limit | 32 GiB / 32 GiB | 15 GiB / 15 GiB |
| `GOMAXPROCS` | 30 | 22 |
| Observed cgroup `cpu.max` | `max 100000` (no quota at the captured cgroup) | `max 100000` (no quota at the captured cgroup) |
| Observed effective CPU set | Not captured | `1-22` (22 CPUs) |

A CPU request influences scheduling; it does not by itself pin the process to that number of CPUs. The ads runtime capture is especially relevant: its effective CPU set contained 22 CPUs, despite its cgroup showing no quota. The supplied Docker ads profile instead uses a 22-CPU quota and does not pin CPUs. Treat that profile as a convenient starting budget, not identical scheduling behavior. `GOMAXPROCS` controls Go execution parallelism; native search pools and other threads also consume CPU.

**An AMD EC2 host is a suitable starting point for this investigation; Kubernetes is not a prerequisite.** Use either source route above or the Docker repro stack below, with a separate generator when possible. Start with the listed search settings and server `GOMAXPROCS`, and report CPU model, SMT/NUMA topology, OS/kernel, CPU affinity/quota, memory limit and execution route with findings. A host-native process can use more resources than these pods unless limits are applied. Whether to add Kubernetes or closer cgroup/CPU-placement matching remains open; first identify useful hotspots and code/compiler improvements. Container or Kubernetes parity can be added later if needed to investigate a scheduling/resource-limit effect.


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
