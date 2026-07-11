#!/bin/sh
set -eu

: "${GH_TOKEN:?GH_TOKEN is required to query GitHub attestations}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"

directory=${1:-attest}

for artifact in "$directory"/*; do
    [ -f "$artifact" ] || continue
    gh attestation verify "$artifact" --repo "$GITHUB_REPOSITORY"
done
