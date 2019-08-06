#!/usr/bin/env sh

# Build a stripped, statically linked binary for linux/amd64
# Must be called from repository root

if [ -z "$1" ]; then
  echo 'Usage: ./scripts/build-production-binary.sh OUTPUT_LOCATION' > /dev/stderr
  exit 1
fi

set -xeuf

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$1"