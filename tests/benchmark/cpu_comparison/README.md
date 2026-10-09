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

HNSW512 raw vectors alone use about **2 GB per million rows**; indexes, build-time working memory, Milvus overhead, and the generator's in-memory dataset require additional headroom. Avoid swapping, especially if generator and server share a host. Start smaller if setup is memory-constrained; the checked-in 200k-row config is a starting point for a smoke test.

## Start Milvus

Choose one launch route at a time; each uses port 19530. Use the published image for an initial smoke test, or a source build to modify Milvus itself.

The official open-source instructions for this version are the [Milvus v2.6.24 development guide](https://github.com/milvus-io/milvus/blob/v2.6.24/DEVELOPMENT.md#building-milvus-on-a-local-osshell-environment) and [Docker builder guide](https://github.com/milvus-io/milvus/blob/v2.6.24/build/README.md). Local copies are [DEVELOPMENT.md](../../../DEVELOPMENT.md) and [build/README.md](../../../build/README.md). The recipes below adapt those guides to this benchmark checkout; use this checkout's `go.mod`, `.env`, and builder Dockerfile for toolchain versions rather than the older minimum versions listed in the guides.

### Docker standalone image

```sh
# docker image
go run ./tests/benchmark/cpu_comparison up
```

### Linux source build in the development builder (recommended)

This gives developers the repository's compiler and dependency environment while allowing edits in the host checkout. The current builder uses Ubuntu 22.04, GCC 12, CMake 3.31.8, Conan 1.64.1, Go 1.25.13, and Rust 1.89. The Linux host can use Ubuntu 24.04; compilation and the Milvus process run inside the builder. CPU instructions execute on the host CPU when the container architecture matches the host.

Install Git, [Docker Engine](https://docs.docker.com/engine/install/ubuntu/), and the [Compose plugin](https://docs.docker.com/compose/install/linux/). Your user must be able to run `docker` commands. The upstream source-build guidance asks for at least 8 GiB RAM and 50 GiB free disk; leave additional space for build caches and benchmark data. Builds download dependencies from public registries and package repositories.

Clone the published benchmark branch from [hicder/milvus](https://github.com/hicder/milvus/tree/nathanwilk7/cpu-benchmarks). Changes awaiting merge are available on the same branch in [nathanwilk7/milvus](https://github.com/nathanwilk7/milvus/tree/nathanwilk7/cpu-benchmarks); use that fork's URL to test those changes before merging them into the published branch.

```sh
git clone --branch nathanwilk7/cpu-benchmarks https://github.com/hicder/milvus.git
cd milvus

# AMD/x86-64 host: uname -m should report x86_64.
# On Graviton/aarch64 use IMAGE_ARCH=arm64 instead; avoid CPU emulation.
export IMAGE_ARCH=amd64
export OS_NAME=ubuntu22.04

# Build the toolchain image from this checkout's Dockerfile.
docker compose -f docker-compose.yml -f tests/benchmark/cpu_comparison/builder-compose.yml build builder

# Enter the source-mounted builder; Compose starts its development dependencies.
docker compose -f docker-compose.yml -f tests/benchmark/cpu_comparison/builder-compose.yml run --service-ports --name cpu-bench-builder builder bash
```

Inside the builder shell, build both the C++ libraries and Go server, then launch your newly built binary in the foreground:

```sh
go version
gcc --version
cmake --version
conan --version
rustc --version

make milvus MILVUS_VERSION=2.6.24
bash -c 'source scripts/setenv.sh; exec ./bin/milvus run standalone'
```

`MILVUS_VERSION=2.6.24` is required for this **2.6.24-based benchmark branch**: the Makefile otherwise labels a branch build with a development version, which the benchmark's exact-version check rejects. This setting controls the reported version; it does not prove source equivalence to the release. Preserve the source commit, local diff, build options, and binary checksum for each experiment. Do not apply this override to a different Milvus release.

In a second terminal on the Linux host, from the same repository root, install [Go](https://go.dev/doc/install) matching the host architecture (at least 1.25.8, as required by `go.mod`), then verify readiness and exercise the build:

```sh
curl --fail http://127.0.0.1:9091/healthz
go build -o /tmp/cpu-bench ./tests/benchmark/cpu_comparison
/tmp/cpu-bench metadata -output /tmp/source-server.json -container cpu-bench-builder -notes 'source build; record commit, patch, binary checksum and build options'
/tmp/cpu-bench setup -workflow hnsw
/tmp/cpu-bench run -workflow hnsw -case hnsw512_not_deleted -duration 20 -qps 10 -concurrency 4 -server-metadata /tmp/source-server.json
```

If health is not ready yet, retry the health check before setup. The builder image is the toolchain image, while `bin/milvus` and its libraries come from the mounted checkout; its image digest alone does not identify the running server binary.

To modify Milvus, edit the host checkout, press Ctrl-C in the builder terminal to stop the server, and rerun the build and launch commands there. Keep the same builder and dependency containers during this loop so the existing collection remains available. Use `run` again for code-only changes; use `reset` when changing dataset/schema/index settings. Recapture metadata to a new filename after each build and attach it to subsequent runs. `make milvus` also regenerates protobuf output, so inspect `git diff` when recording your patch.

For incremental builds, `make milvus MILVUS_VERSION=2.6.24 SKIP_3RDPARTY=1` skips third-party dependency installation/build after a successful full build. Omit that flag when changing dependency definitions or switching build settings. The default C++ mode is `Release`; use `mode=RelWithDebInfo` for profiling with symbols and record that choice on both machines.

On Graviton, also confirm ARM64 support for the dependency images in the root Compose file; changing `IMAGE_ARCH` selects the builder architecture only.

### Native Linux source build

For a host-native build, first follow the [official dependency setup](https://github.com/milvus-io/milvus/blob/v2.6.24/DEVELOPMENT.md#installing-dependencies). This checkout requires Go >= 1.25.8 and Conan **1.x**, and the builder above provides a concrete reference toolchain. The upstream `scripts/install_deps.sh` installs system packages, Conan, CMake, and Rust, but does not install Go or select GCC 12 automatically. It targets older Ubuntu releases and uses `sudo pip3`; on Ubuntu 24.04 prefer the builder route, or provision the equivalent toolchain with Conan in an isolated Python environment. The upstream FAQ recommends Python <= 3.11 for older Conan dependencies.

After installing and verifying those tools, run from the repository root:

```sh
# Start only etcd and MinIO; do not start the published standalone image.
docker compose -f tests/benchmark/cpu_comparison/docker-compose.yml up -d etcd minio
make milvus MILVUS_VERSION=2.6.24

# The default local-storage paths are under /var/lib/milvus.
# On a dedicated benchmark host, create them for the current user once.
sudo install -d -o "$(id -u)" -g "$(id -g)" /var/lib/milvus

ETCD_ENDPOINTS=127.0.0.1:2379 MINIO_ADDRESS=127.0.0.1:9000 \
  bash -c 'source scripts/setenv.sh; exec ./bin/milvus run standalone'
```

Use the second-terminal readiness/setup/run commands above, omitting `-container` from metadata and recording binary provenance and process resource limits in `-notes`. If Conan 1.x is installed as `conan-1`, prefix the build with `CONAN_CMD=conan-1`. Rebuild and restart after edits, as with the builder route. Binaries built in the builder may require its libraries/glibc environment; use the native route when you need a host-native executable.

### Modify Knowhere and other dependencies

`make milvus` builds Knowhere and the other CMake source dependencies together with Milvus; you do not need to build/install Knowhere separately. In this checkout, the [Knowhere integration](../../../internal/core/thirdparty/knowhere/CMakeLists.txt) fetches `https://github.com/zilliztech/knowhere.git` at **v2.6.21**. This is the Knowhere version used by this Milvus 2.6.24 checkout; the two projects have independent version numbers.

For local Knowhere edits, use a separate checkout and CMake's [source-directory override](https://cmake.org/cmake/help/v3.31/module/FetchContent.html#variable:FETCHCONTENT_SOURCE_DIR_%3CuppercaseName%3E). Run these commands from the Milvus repository root, either inside the builder shell or in the prepared native build environment:

```sh
# Kept inside the source mount, but outside make clean's build directories.
mkdir -p .docker/dependency-src
git clone --branch v2.6.21 https://github.com/zilliztech/knowhere.git .docker/dependency-src/knowhere
git -C .docker/dependency-src/knowhere switch -c amd-experiment

# Edit .docker/dependency-src/knowhere, then rebuild the full server.
# Set this in the shell that runs make; the path must exist inside the builder.
export CMAKE_EXTRA_ARGS="-DFETCHCONTENT_SOURCE_DIR_KNOWHERE=${PWD}/.docker/dependency-src/knowhere"
make milvus MILVUS_VERSION=2.6.24
```

The override bypasses CMake's download/update of Knowhere and builds your local checkout. Keep it set for each rebuild. Stop the old Milvus process first, then restart with the launch command for your chosen route. After a successful full build, source-only Knowhere edits can use `make milvus MILVUS_VERSION=2.6.24 SKIP_3RDPARTY=1`: that skips the Conan installation phase, **not** Knowhere compilation. Leave it off if dependency requirements or build settings change.

For HNSW work, Knowhere's wrapper is `src/index/hnsw/faiss_hnsw.cc` and its vendored Faiss implementation is under `thirdparty/faiss/`, both inside the separate checkout. Edits there are compiled through the same Milvus build; installing a Python Faiss package does not change the C++ library Milvus uses. For Knowhere unit tests and standalone development, follow its [v2.6.21 README](https://github.com/zilliztech/knowhere/blob/v2.6.21/README.md) (Conan 1.x, `with_ut=True`, then the `knowhere_tests` executable); the ordinary Milvus build does not enable Knowhere's tests by default. Keep a standalone Knowhere test build separate from Milvus's `cmake_build` tree.

Confirm the source selected by CMake and record the local dependency's identity:

```sh
rg '^FETCHCONTENT_SOURCE_DIR_KNOWHERE:' cmake_build/CMakeCache.txt
git -C .docker/dependency-src/knowhere rev-parse HEAD
git -C .docker/dependency-src/knowhere diff --binary > /tmp/knowhere-experiment.patch
sha256sum bin/milvus internal/core/output/lib/libknowhere.so
```

Also preserve new/untracked files or commit them locally in the Knowhere checkout. `.docker/` is ignored by the Milvus repository, so Milvus's `git diff` and metadata snapshot do not include your Knowhere patch. Record the dependency commit/patch and built library checksum alongside the run results. If the shared library has a different installed filename, locate it under `internal/core/output/lib` and checksum that file.

To return to the automatically fetched baseline, **clear the cached override explicitly**, then rebuild and restart:

```sh
export CMAKE_EXTRA_ARGS='-DFETCHCONTENT_SOURCE_DIR_KNOWHERE='
make milvus MILVUS_VERSION=2.6.24
unset CMAKE_EXTRA_ARGS
```

Unsetting the environment variable alone does not remove an override already saved in `CMakeCache.txt`. For a fully clean rebuild, stop Milvus, save your patches/results, run `make clean`, and rerun the full build with the intended override/settings. `make clean` deletes `bin/`, `lib/`, `cmake_build/`, and `internal/core/output/`; it keeps the separate `.docker/dependency-src/knowhere` checkout. Avoid editing the default fetched source under `cmake_build/`, where cleanup or dependency updates can remove your changes.

Other dependency entry points:

| Dependency | Where to edit or pin it | Rebuild guidance |
| --- | --- | --- |
| milvus-common (`54cb8fe`) | [CMake integration](../../../internal/core/thirdparty/milvus-common/CMakeLists.txt) | Same FetchContent approach; the override is `FETCHCONTENT_SOURCE_DIR_MILVUS-COMMON`. |
| milvus-storage (`1f14008`) | [CMake integration](../../../internal/core/thirdparty/milvus-storage/CMakeLists.txt) | Use `FETCHCONTENT_SOURCE_DIR_MILVUS-STORAGE` pointing at the repository root; the integration selects its `cpp` subdirectory. |
| Arrow, RocksDB, OpenBLAS, protobuf, and other Conan packages | [Conan requirements/options](../../../internal/core/conanfile.py) and [installation script](../../../scripts/3rdparty_build.sh) | Update the package/recipe revision and build without `SKIP_3RDPARTY`. Locally patched packages need their own Conan 1.x recipe/reference; `--build=missing` can reuse an existing cached binary and does not force a patched rebuild. |
| Tantivy/Rust bindings | [Binding sources and Cargo manifest/lock](../../../internal/core/thirdparty/tantivy/tantivy-binding) and [CMake integration](../../../internal/core/thirdparty/tantivy/CMakeLists.txt) | Edit the binding sources directly; preserve Cargo revision/lock changes. `make milvus` invokes Cargo with Rust 1.89. |
| Go libraries | Root `go.mod`/`go.sum`, plus the `pkg` and `client` module files when relevant | Update the appropriate module and rebuild Milvus; rebuild the benchmark CLI too if its dependencies changed. |

Preserve the same build mode and dependency baseline between AMD/Graviton runs. Changing a dependency pin may require corresponding Milvus API changes and dependency tests. These are source/build instructions, not a claim that a modified dependency has passed its own tests or the Linux benchmark.

### Distance kernels, SIMD, and build options

Record both **what was compiled** and **what the running server selected**. These settings can affect distance-calculation speed and numerical results, and should be part of each AMD/Graviton experiment's metadata. Start with the defaults on each architecture, then change one setting at a time.

| Setting | This checkout's behavior | How to experiment |
| --- | --- | --- |
| C++ build mode | `mode=Release` by default | Use `mode=RelWithDebInfo` for profiling with symbols; keep the mode consistent across a comparison. |
| x86 distance kernels | Knowhere v2.6.21 builds SSE4.2, AVX2, and AVX-512 variants, with per-target compiler flags | Use the runtime SIMD setting below for initial kernel comparisons. For compiler/kernel changes, edit the local Knowhere checkout's `cmake/libs/libfaiss.cmake` and `src/simd/`, then rebuild Milvus. |
| ARM distance kernels | The same Knowhere build includes NEON and compiler-dependent SVE support; runtime hooks check CPU capabilities | Use `auto` on Graviton. Preserve compiler checks/flags and runtime selection logs; compiler support alone does not prove the CPU supports an instruction set. Some operations still use NEON when the selected path reports SVE. |
| `USE_DYNAMIC_SIMD` | Milvus's Makefile defaults to `ON` and forwards a CMake macro | Record this flag. `OFF` does **not** establish a scalar-only build: Knowhere's distance-kernel targets and hooks are defined separately. Verify the generated compile flags and actual kernel selection before attributing a performance difference to this switch. |
| `CPU_TARGET` / `CPU_ARCH` | `scripts/core_build.sh` detects broad targets such as `avx`, `sse`, or `aarch64`, then passes `CPU_ARCH` to CMake | These values are not an AVX2-versus-AVX-512 runtime selector. Inspect the actual dependency target flags when tuning a build; avoid adding a global `-march=native` without checking all targets and the CPUs where the binary will run. |
| BLAS | The Linux Faiss integration selects OpenBLAS; the Conan recipe enables `openblas:dynamic_arch` | Library version, build options, and threading may matter for BLAS-backed paths. Changing the package requires the Conan dependency workflow above. This runner sends one query vector per request, so its results do not establish batched BLAS performance. |
| Score computation | `queryNode.segcore.knowhereScoreConsistency` defaults to `false`; enabling it invokes Knowhere's FP32-as-BF16 score-computation patch | Treat this as a numerical-behavior experiment, preserve the setting, and validate scores/recall as well as latency. The benchmark's single-query recall check is insufficient to establish accuracy equivalence. |

The primary runtime control is [common.simdType](../../../configs/milvus.yaml), accepted by [Milvus's Knowhere adapter](../../../internal/core/src/config/ConfigKnowhere.cpp). To compare AVX2 with the default on an AMD host, merge this into `configs/user.yaml` without replacing existing settings, then restart Milvus:

```yaml
common:
  simdType: avx2
```

Valid values in this version are `auto`, `avx512`, `avx2`, `avx`, and `sse4_2`. `avx` is an alias for `sse4_2` in this adapter. On x86, `auto` and `avx512` both allow AVX-512 with fallback to supported lower instruction sets; `avx2` excludes AVX-512 from the configured distance hooks. `generic`, `neon`, and `sve` are **not** accepted Milvus configuration values in this version, even though Knowhere has implementations with those names. On ARM, the x86 selection flags do not control the NEON/SVE hook; keep the setting at `auto`.

Changing this startup configuration does not require a rebuild. Inspect startup logs for `FAISS hook` and the reported SIMD selection, and retain them with the run. A hook label describes dispatch for the configured functions, not a guarantee that every operation in every index uses that ISA; specialized kernels and BLAS have their own paths. Inspect this pinned Knowhere source's `src/simd/hook.cc` and `cmake/libs/libfaiss.cmake` when narrowing the scope of an experiment.

For an explicit build baseline and metadata record:

```sh
make milvus MILVUS_VERSION=2.6.24 mode=Release USE_DYNAMIC_SIMD=ON
# Run on the Linux host; use a fresh output filename for each experiment.
/tmp/cpu-bench metadata -output /tmp/source-simd-baseline.json -container cpu-bench-builder \
  -build-flags 'MILVUS_VERSION=2.6.24 mode=Release USE_DYNAMIC_SIMD=ON' \
  -notes 'Record Knowhere commit/patch, compiler flags, BLAS settings and actual SIMD selection'
```

Include any `CMAKE_EXTRA_ARGS` source overrides and custom compiler settings in the recorded build flags. For native Milvus, omit `-container`. If attaching `-server-config`, supply sanitized configuration including the SIMD and score settings; this tool copies the file verbatim and does not collect effective configuration automatically. The baseline command and metadata example do not replace validating the compiled binary on the target CPU.

### Stop a source build

Press Ctrl-C to stop the foreground Milvus process. For the native route, `go run ./tests/benchmark/cpu_comparison down` stops its benchmark dependencies. For the builder route, exit the shell and stop its separate development stack:

```sh
docker compose -f docker-compose.yml -f tests/benchmark/cpu_comparison/builder-compose.yml down --remove-orphans
```

The root development stack does not persist etcd/MinIO data in named volumes, so this builder cleanup discards that stack's collection state; rerun setup after starting it again. Local dataset bundles/results remain in the checkout. The benchmark `up`/`down` commands manage only the small standalone stack, not the builder stack.

These source-build recipes were checked against this checkout's build files; a full Linux source build and benchmark run still need to be verified on the target AMD/Graviton machines.

## TODO

- [ ] review production Milvus scalar indexes, search parameters, segment sizes, thread pools, mmap, consistency, ignore-growing, partition selection, and group-by settings. Adapt relevant settings for a local single-node CPU benchmark; production settings target distributed Kubernetes deployments.
