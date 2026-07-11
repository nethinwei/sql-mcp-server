#!/usr/bin/env python3

import argparse
import json
from pathlib import Path


def load(path: Path) -> dict:
    with path.open(encoding="utf-8") as source:
        return json.load(source)


def render(document: dict, version: str) -> dict:
    document["version"] = version
    for package in document.get("packages", []):
        if package.get("registryType") != "oci":
            package["version"] = version
            continue
        package.pop("version", None)
        identifier = package.get("identifier", "")
        if "@" in identifier or ":" not in identifier:
            raise ValueError(f"OCI identifier must contain a mutable version tag: {identifier}")
        image, _ = identifier.rsplit(":", 1)
        package["identifier"] = f"{image}:{version}"
    return document


def verify(document: dict, version: str) -> None:
    if document.get("version") != version:
        raise ValueError(f"server version is {document.get('version')!r}, expected {version!r}")
    for index, package in enumerate(document.get("packages", [])):
        if package.get("registryType") != "oci":
            if package.get("version") != version:
                raise ValueError(f"package {index} version does not match {version}")
            continue
        if "version" in package:
            raise ValueError(f"OCI package {index} must not contain a version field")
        if not package.get("identifier", "").endswith(f":{version}"):
            raise ValueError(f"OCI package {index} identifier does not end with :{version}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("render", "verify"))
    parser.add_argument("--file", default="server.json", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()

    document = load(args.file)
    if args.command == "render":
        if args.output is None:
            parser.error("render requires --output")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        rendered = render(document, args.version)
        verify(rendered, args.version)
        args.output.write_text(json.dumps(rendered, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return
    verify(document, args.version)


if __name__ == "__main__":
    main()
