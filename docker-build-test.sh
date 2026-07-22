#!/bin/bash
set -e
cd "$(dirname "$0")"
echo "=== Building ==="
docker run --rm -v "$(pwd):/workspace" -w /workspace golang:1.22 go build ./...
echo "=== Testing ==="
docker run --rm -v "$(pwd):/workspace" -w /workspace golang:1.22 go test ./...
echo "=== All passed ==="
