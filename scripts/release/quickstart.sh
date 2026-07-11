#!/bin/sh
set -eu

compose_file=${COMPOSE_FILE:-examples/quickstart/compose.yaml}

cleanup() {
    docker compose -f "$compose_file" down -v
}
trap cleanup EXIT

docker compose -f "$compose_file" up -d --wait
go run ./internal/quickstartsmoke
