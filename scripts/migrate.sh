#!/bin/bash
set -e

cd "$(dirname "$0")/.."

go run ./cmd/manager -migrate-only
