#!/usr/bin/env bash
# Local CI gate: format, vet, build, test. Run from anywhere; cds to repo root.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

echo "==> gofmt -l ."
unformatted="$(gofmt -l .)"
if [ -n "${unformatted}" ]; then
  echo "ERROR: the following files are not gofmt-clean:"
  echo "${unformatted}"
  exit 1
fi

# `go vet/build/test ./...` exit non-zero while the module has no packages yet
# ("matched no packages"), so skip them until the first package lands.
pkgs="$(go list ./... 2>/dev/null || true)"
if [ -z "${pkgs}" ]; then
  echo "==> no Go packages yet; skipping vet/build/test"
else
  echo "==> go vet ./..."
  go vet ./...

  echo "==> go build ./..."
  go build ./...

  echo "==> go test ./... -count=1"
  go test ./... -count=1
fi

echo "CI OK"
