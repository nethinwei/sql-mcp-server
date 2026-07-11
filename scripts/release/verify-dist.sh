#!/bin/sh
set -eu

dist=${1:-dist}

(cd "$dist" && sha256sum --check checksums.txt)

count_matches() {
    count=0
    for artifact in $1; do
        [ -e "$artifact" ] || continue
        count=$((count + 1))
    done
    [ "$count" -eq "$2" ] || {
        echo "expected $2 artifacts matching $1, found $count" >&2
        exit 1
    }
}

count_matches "$dist/sql-mcp-server_*_linux_*.tar.gz" 2
count_matches "$dist/sql-mcp-server_*_darwin_*.tar.gz" 2
count_matches "$dist/sql-mcp-server_*_windows_*.zip" 2
