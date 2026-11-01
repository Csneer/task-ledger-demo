#!/usr/bin/env python3
"""Collect public Go module metadata and verbatim distribution notices, not an SBOM."""

import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess


NOTICE_NAME = re.compile(r"^(LICENSE|LICENCE|COPYING|NOTICE|COPYRIGHT|PATENTS)($|[._-])", re.I)
PUBLIC_FIELDS = ("Path", "Version", "Sum", "GoModSum", "GoVersion")


def json_stream(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        value, end = decoder.raw_decode(raw)
        yield value
        raw = raw[end:]


def copy_notices(source, destination):
    notices = sorted(p for p in source.rglob("*")
                     if p.is_file() and NOTICE_NAME.match(p.name)
                     and p.suffix not in (".go", ".c", ".h"))
    if not any(p.parent == source for p in notices):
        raise ValueError(f"No root distribution notice found for {source.name}")
    for notice in notices:
        target = destination / notice.relative_to(source)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(notice, target)
    return [str(p.relative_to(source)) for p in notices]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--inventory", type=Path, required=True)
    parser.add_argument("--notices", type=Path, required=True)
    args = parser.parse_args()
    # The complete graph includes test/build dependencies which a normal build
    # may not download. Preserve their notices without implying they are linked.
    subprocess.run([args.go, "mod", "download", "all"], check=True)
    raw = subprocess.check_output([args.go, "list", "-m", "-json", "all"], text=True)
    modules = sorted((m for m in json_stream(raw) if not m.get("Main")),
                     key=lambda m: m["Path"])
    args.notices.mkdir(parents=True, exist_ok=False)
    index = []
    for module in modules:
        if module.get("Replace"):
            raise ValueError("Release inventory does not support replaced modules")
        source = Path(module["Dir"])
        relative = Path("modules") / (module["Path"] + "@" + module["Version"])
        files = copy_notices(source, args.notices / relative)
        index.append({"module": module["Path"], "version": module["Version"],
                      "directory": relative.as_posix(), "notices": files})
    goroot = Path(subprocess.check_output([args.go, "env", "GOROOT"], text=True).strip())
    toolchain = args.notices / "go"
    toolchain.mkdir()
    shutil.copyfile(goroot / "LICENSE", toolchain / "LICENSE")
    if (goroot / "PATENTS").is_file():
        shutil.copyfile(goroot / "PATENTS", toolchain / "PATENTS")
    # The standard library vendors crypto/network dependencies separately from
    # the application's module graph. Retain their license and patent notices.
    vendor = goroot / "src" / "vendor"
    for notice in sorted(vendor.rglob("*")):
        if notice.is_file() and NOTICE_NAME.match(notice.name):
            target = toolchain / "vendor" / notice.relative_to(vendor)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(notice, target)
    (args.notices / "index.json").write_text(json.dumps(index, indent=2) + "\n")
    (args.notices / "README.txt").write_text(
        "Verbatim notices for the full pinned Go module graph and Go standard library.\n"
        "This includes test/build-only dependencies; inclusion does not imply binary linkage.\n"
        "Module-relative paths are preserved. See index.json and go-dependencies.json.\n"
        "This collection and dependency inventory are not an SBOM or a license audit.\n")
    inventory = {"kind": "Go module graph inventory (not an SBOM)",
                 "go_version": subprocess.check_output([args.go, "env", "GOVERSION"], text=True).strip(),
                 "modules": [{k: m[k] for k in PUBLIC_FIELDS if k in m} for m in modules]}
    args.inventory.write_text(json.dumps(inventory, indent=2) + "\n")


if __name__ == "__main__":
    main()
