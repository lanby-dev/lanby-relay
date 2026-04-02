#!/usr/bin/env bash
set -euo pipefail

echo "==> Formatting"
go fmt ./...

echo "==> Testing"
go test ./...

echo "==> Done"
