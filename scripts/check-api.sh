#!/usr/bin/env bash
set -euo pipefail

apidiff="${APIDIFF:-.bin/apidiff}"
baseline="api/v3.export"
package="github.com/roteiro-gis/geo-crdt"

report="$("$apidiff" -incompatible "$baseline" "$package")"
if [[ -n "$report" ]]; then
  echo "incompatible public API changes:"
  echo "$report"
  exit 1
fi
