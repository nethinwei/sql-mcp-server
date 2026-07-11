#!/bin/sh
set -eu

image=$1
mode=${2:-local}
expected=${MCP_SERVER_NAME:-io.github.nethinwei/sql-mcp-server}

if [ "$mode" = "remote" ]; then
    actual=$(
        docker buildx imagetools inspect "$image" \
            --format '{{json (index .Image "linux/amd64").Config.Labels}}' \
            | python3 -c 'import json,sys; print(json.load(sys.stdin).get("io.modelcontextprotocol.server.name", ""))'
    )
else
    actual=$(
        docker image inspect "$image" \
            --format '{{index .Config.Labels "io.modelcontextprotocol.server.name"}}'
    )
fi

[ "$actual" = "$expected" ] || {
    echo "MCP image label is '$actual', expected '$expected'" >&2
    exit 1
}
