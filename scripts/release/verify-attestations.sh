#!/bin/sh
set -eu

: "${GH_TOKEN:?GH_TOKEN is required to query GitHub attestations}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"

directory=${1:-attest}

count=0
for artifact in "$directory"/*; do
    [ -f "$artifact" ] || continue
    gh attestation verify "$artifact" --repo "$GITHUB_REPOSITORY"
    count=$((count + 1))
done
[ "$count" -gt 0 ] || {
    echo "no artifacts to verify in $directory" >&2
    exit 1
}
echo "verified $count artifacts"
