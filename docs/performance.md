# Performance release gate

CI runs `BenchmarkLargePolygonMoves`, the representative workload that merges
1,000 moves into a 5,000-point polygon, three times with fixed iteration
counts. A release fails if any run exceeds:

- 25 ms per operation
- 8 MB allocated per operation
- 60,000 allocations per operation

The ceilings allow normal hosted-runner variance while detecting material
regressions from the current roughly 6.5–8.8 ms, 5.2 MB, and 42,000
allocations on Apple M1. Override the environment variables in
`scripts/check-benchmarks.sh` only for diagnostic runs; release CI uses the
committed ceilings.
