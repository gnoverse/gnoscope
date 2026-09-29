#!/bin/sh
set -e
HASH=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BTIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
# Falls back to the short hash until this repo carries a tag, so the version
# row in the UI always says something rather than "dev" on a real build.
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "$HASH")
CGO_ENABLED=0 go build -ldflags "-X main.gitHash=$HASH -X main.buildTime=$BTIME -X main.version=$VERSION" -o "${1:-gnoscope}" .
