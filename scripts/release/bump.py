#!/usr/bin/env python3
"""Rewrite every pinned release-version reference in one idempotent pass.

Usage: scripts/release/bump.py --version 0.1.7

Each rule must match exactly once; zero or multiple matches abort with an
error so silent drift between the reference points is impossible. Editorial
files (CHANGELOG, release notes, roadmap) are intentionally out of scope.
"""

import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def rules(version: str) -> list[tuple[Path, re.Pattern[str], str]]:
    v = re.escape(version)
    semver = r"\d+\.\d+\.\d+"
    return [
        (
            ROOT / "server.json",
            re.compile(rf'"version": "{semver}"'),
            f'"version": "{version}"',
        ),
        (
            ROOT / "server.json",
            re.compile(rf'"identifier": "ghcr\.io/nethinwei/sql-mcp-server:{semver}"'),
            f'"identifier": "ghcr.io/nethinwei/sql-mcp-server:{version}"',
        ),
        (
            ROOT / "examples/quickstart/compose.yaml",
            re.compile(rf"ghcr\.io/nethinwei/sql-mcp-server:v{semver}"),
            f"ghcr.io/nethinwei/sql-mcp-server:v{version}",
        ),
        (
            ROOT / "examples/modelscope/docker-mcp-config.json",
            re.compile(rf'"ghcr\.io/nethinwei/sql-mcp-server:{semver}"'),
            f'"ghcr.io/nethinwei/sql-mcp-server:{version}"',
        ),
        (
            ROOT / "README.md",
            re.compile(rf"当前 GA：`v{semver}`（\[发布说明\]\(docs/releases/v{semver}\.md\)）"),
            f"当前 GA：`v{version}`（[发布说明](docs/releases/v{version}.md)）",
        ),
        (
            ROOT / "docs/releases/README.md",
            re.compile(rf"当前 GA 为 `v{semver}`（\[发布说明\]\(v{semver}\.md\)、"),
            f"当前 GA 为 `v{version}`（[发布说明](v{version}.md)、",
        ),
        (
            ROOT / "docs/releases/README.md",
            re.compile(rf"releases/tag/v{semver}\)）。"),
            f"releases/tag/v{version})）。",
        ),
    ]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True, help="bare semver, e.g. 0.1.7")
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+", args.version):
        parser.error(f"--version must be bare semver, got {args.version!r}")

    failures = []
    changed = set()
    for path, pattern, replacement in rules(args.version):
        text = path.read_text(encoding="utf-8")
        matches = pattern.findall(text)
        if len(matches) != 1:
            failures.append(f"{path.relative_to(ROOT)}: pattern {pattern.pattern!r} matched {len(matches)} times, want 1")
            continue
        updated = pattern.sub(replacement, text, count=1)
        if updated != text:
            path.write_text(updated, encoding="utf-8")
            changed.add(path.relative_to(ROOT))
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    for path in sorted(str(p) for p in changed):
        print(f"bumped {path}")
    if not changed:
        print(f"all references already at {args.version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
