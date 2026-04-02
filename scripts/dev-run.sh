#!/usr/bin/env bash
set -euo pipefail

# Local development defaults (override via env)
export PLATFORM_URL="${PLATFORM_URL:-http://localhost:8080}"
export AGENT_VERSION="${AGENT_VERSION:-dev}"
export IDENTITY_PATH="${IDENTITY_PATH:-./.data/identity.json}"
export CONFIG_POLL_SECONDS="${CONFIG_POLL_SECONDS:-30}"

echo "Starting lanby-relay with:"
echo "  PLATFORM_URL=$PLATFORM_URL"
echo "  AGENT_VERSION=$AGENT_VERSION"
echo "  IDENTITY_PATH=$IDENTITY_PATH"
echo "  CONFIG_POLL_SECONDS=$CONFIG_POLL_SECONDS"

go run ./cmd/relay
