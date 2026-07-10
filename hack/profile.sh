#!/usr/bin/env bash
# Builds the scaletest binary, runs it with a CPU profile enabled, and opens
# the profile in `go tool pprof`.
#
# Usage:
#   hack/profile.sh [pprof-mode] [-- scaletest-render-flags...]
#
# pprof-mode is passed to `go tool pprof` and defaults to -top. Use -http=:0
# to open the interactive web UI, or -web for a native image viewer.
#
# Examples:
#   hack/profile.sh
#   hack/profile.sh -http=:0
#   hack/profile.sh -top -- --renderer=parallel --nodes=500 --pods-per-node=200

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

pprof_mode="-top"
if [[ $# -gt 0 && "$1" != "--" ]]; then
  pprof_mode="$1"
  shift
fi
if [[ "${1:-}" == "--" ]]; then
  shift
fi

bin_dir="$repo_root/bin"
profile_out="$repo_root/bin/scaletest.prof"
mkdir -p "$bin_dir"

echo "building bin/scaletest" >&2
go build -o "$bin_dir/scaletest" ./cmd/scaletest

echo "running scaletest render --cpuprofile=$profile_out $*" >&2
"$bin_dir/scaletest" render --cpuprofile="$profile_out" "$@"

echo "opening profile with: go tool pprof $pprof_mode" >&2
go tool pprof "$pprof_mode" "$bin_dir/scaletest" "$profile_out"
