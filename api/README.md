# API compatibility baseline

`v3.export` records the public Go API after the protocol-v3 redesign. CI
compares it to the current package with the pinned Go `apidiff` command and
rejects incompatible removals, signature changes, and interface changes.

Regenerate the baseline only for an intentional breaking API release:

```sh
apidiff -w api/v3.export github.com/roteiro-gis/geo-crdt
```
