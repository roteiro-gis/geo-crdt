#!/usr/bin/env bash
set -euo pipefail

max_ns_per_op="${MAX_NS_PER_OP:-25000000}"
max_bytes_per_op="${MAX_BYTES_PER_OP:-8000000}"
max_allocs_per_op="${MAX_ALLOCS_PER_OP:-60000}"

output="$(go test -run '^$' -bench '^BenchmarkLargePolygonMoves$' \
  -benchmem -benchtime=20x -count=3 .)"
echo "$output"

awk \
  -v max_ns="$max_ns_per_op" \
  -v max_bytes="$max_bytes_per_op" \
  -v max_allocs="$max_allocs_per_op" '
    /^BenchmarkLargePolygonMoves-/ {
      found = 1
      if ($3 > max_ns) {
        printf "benchmark exceeded time ceiling: %s ns/op > %s ns/op\n", $3, max_ns
        failed = 1
      }
      if ($5 > max_bytes) {
        printf "benchmark exceeded allocation ceiling: %s B/op > %s B/op\n", $5, max_bytes
        failed = 1
      }
      if ($7 > max_allocs) {
        printf "benchmark exceeded allocation-count ceiling: %s allocs/op > %s allocs/op\n", $7, max_allocs
        failed = 1
      }
    }
    END {
      if (!found) {
        print "benchmark result was not found"
        exit 1
      }
      exit failed
    }
  ' <<<"$output"
