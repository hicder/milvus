#!/usr/bin/env bash
# Run on the verified server host after the runner announces "measuring".
set -euo pipefail
if [[ $# -lt 3 || $# -gt 4 ]]; then
  echo "usage: $0 HOST_MILVUS_PID CASE QPS [fp|dwarf]" >&2
  exit 2
fi
perf_pid=$1
perf_case=$2
perf_qps=$3
perf_unwind=${4:-fp}
[[ $perf_pid =~ ^[1-9][0-9]*$ && $perf_qps =~ ^[1-9][0-9]*$ && $perf_case =~ ^[a-z0-9_]+$ ]] || exit 2
case "$perf_unwind" in
  fp) perf_callgraph=fp ;;
  dwarf) perf_callgraph=dwarf,16384 ;;
  *) echo "unwind must be fp or dwarf" >&2; exit 2 ;;
esac
perf_cmdline=$(sudo cat "/proc/$perf_pid/cmdline" | tr '\0' ' ')
if [[ $perf_cmdline != *"milvus run standalone"* ]]; then
  echo "Refusing to profile PID $perf_pid: expected our Milvus standalone process" >&2
  exit 1
fi
perf_dir=$(mktemp -d "$HOME/amd-perf-${perf_case}-${perf_qps}qps-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
{
  date -u +%FT%TZ
  hostname
  uname -srmo
  perf --version
  printf 'pid=%s\ncase=%s\noffered_qps=%s\nfrequency=99\nunwind=%s\n' "$perf_pid" "$perf_case" "$perf_qps" "$perf_callgraph"
  printf 'cmdline=%s\n' "$perf_cmdline"
} > "$perf_dir/metadata.txt"
date -u +%FT%TZ > "$perf_dir/record-start.txt"
sudo perf record -o "$perf_dir/perf.data" -F 99 --call-graph "$perf_callgraph" -p "$perf_pid" -- sleep 30
date -u +%FT%TZ > "$perf_dir/record-end.txt"
sudo perf script -i "$perf_dir/perf.data" --symfs="/proc/$perf_pid/root" > "$perf_dir/out.perf"
sudo chown "$(id -u):$(id -g)" "$perf_dir/perf.data"
perf_flamegraph=${FLAMEGRAPH_DIR:-"$HOME/FlameGraph"}
if [[ -x "$perf_flamegraph/stackcollapse-perf.pl" && -x "$perf_flamegraph/flamegraph.pl" ]]; then
  "$perf_flamegraph/stackcollapse-perf.pl" "$perf_dir/out.perf" > "$perf_dir/out.folded"
  perl -a -n -e '
    next unless /^(.*)\s+(\d+)$/;
    my ($stack, $count) = ($1, $2);
    my @f = split /;/, $stack;
    shift @f;
    print join(";", @f), " $count\n";
  ' "$perf_dir/out.folded" > "$perf_dir/out.no-thread.folded"
  "$perf_flamegraph/flamegraph.pl" --title "$perf_case at $perf_qps offered QPS" "$perf_dir/out.no-thread.folded" > "$perf_dir/flamegraph.svg"
else
  echo "Raw capture saved; FlameGraph tools not found at $perf_flamegraph" >&2
fi
printf 'Capture saved: %s\n' "$perf_dir"
