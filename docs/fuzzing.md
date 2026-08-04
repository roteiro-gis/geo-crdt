# Persisted fuzz corpora

Each fuzz target has committed regression inputs under
`testdata/fuzz/<target>`. The geometry, operation, and delta inputs were
selected from the local corpus produced by the pre-v1 review fuzz campaign;
the snapshot corpus records malformed structural cases from the snapshot
validation finding.

Run one target with a single worker:

```sh
go test -run=^$ -parallel=1 -fuzz=FuzzMergeDelta -fuzztime=10s .
```

When fuzzing finds a useful non-crashing coverage input, copy a small,
representative case into the matching committed corpus. Crashing inputs
written to that directory by `go test` are regression tests automatically
and must remain committed with the fix.
