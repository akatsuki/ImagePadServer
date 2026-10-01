"""Exclusive run directories and JSON-only records for CPU20% experiments."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import statistics
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable


ROOT = Path(__file__).resolve().parents[3]
PROFILES = {"production30", "legacy60"}
SCHEMA_VERSION = 1


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str | None:
    if not path.is_file():
        return None
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _git(*args: str) -> str | None:
    try:
        result = subprocess.run(
            ["git", *args], cwd=ROOT, capture_output=True, text=True, check=False
        )
    except OSError:
        return None
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def dirty_file_hashes() -> dict[str, str | None]:
    # Untracked experiment/build trees can be very large. Tracked dirty files
    # are the stable source snapshot; new run artifacts are identified by the
    # run directory itself and are not recursively enumerated here.
    status = _git("status", "--short", "--untracked-files=no") or ""
    hashes: dict[str, str | None] = {}
    for line in status.splitlines():
        if len(line) < 4:
            continue
        path_text = line[3:]
        if " -> " in path_text:
            path_text = path_text.split(" -> ")[-1]
        path = ROOT / path_text
        if path.is_file():
            hashes[path_text] = sha256_file(path)
    return hashes


def identity_for(path_text: str | None) -> dict[str, Any]:
    if not path_text:
        return {"path": None, "sha256": None, "exists": False}
    path = Path(path_text).expanduser().resolve()
    return {"path": str(path), "sha256": sha256_file(path), "exists": path.exists()}


def environment_manifest() -> dict[str, Any]:
    return {
        "head": _git("rev-parse", "HEAD"),
        "git_describe": _git("describe", "--always", "--dirty"),
        "dirty_file_hashes": dirty_file_hashes(),
        "os": platform.platform(),
        "python": sys.version,
        "logical_cpus": os.cpu_count(),
    }


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    data = (json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8")
    with tempfile.NamedTemporaryFile("wb", dir=path.parent, prefix=f".{path.name}.", delete=False) as stream:
        temporary = Path(stream.name)
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    try:
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def _read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def create_run(
    run_root: Path,
    run_id: str,
    profile: str,
    *,
    artifacts: dict[str, str | None] | None = None,
    metadata: dict[str, Any] | None = None,
) -> Path:
    if not run_id or "/" in run_id or "\\" in run_id or run_id in {".", ".."}:
        raise ValueError(f"invalid run id: {run_id!r}")
    if profile not in PROFILES:
        raise ValueError(f"unknown profile: {profile!r}; expected one of {sorted(PROFILES)}")
    run_root = Path(run_root)
    run_root.mkdir(parents=True, exist_ok=True)
    run_dir = run_root / run_id
    run_dir.mkdir(parents=False, exist_ok=False)
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "profile": profile,
        "status": "pending",
        "started_at": utc_now(),
        "finished_at": None,
        "budget": {"requested_percent": 20, "verified": False},
        "pids": [],
        "cpu_samples": [],
        "timers_ms": {},
        "validation": {"decode": None, "pts": None, "slice": None, "pixels": None},
        "invalid_reason": None,
        "environment": environment_manifest(),
        "artifacts": {name: identity_for(path) for name, path in (artifacts or {}).items()},
        "metadata": metadata or {},
    }
    _write_json(run_dir / "manifest.json", manifest)
    return run_dir


def record_case(run_dir: Path, case_id: str, result: dict[str, Any]) -> Path:
    if not case_id or "/" in case_id or "\\" in case_id:
        raise ValueError(f"invalid case id: {case_id!r}")
    run_dir = Path(run_dir)
    manifest_path = run_dir / "manifest.json"
    if not manifest_path.is_file():
        raise FileNotFoundError(f"run manifest not found: {manifest_path}")
    case_dir = run_dir / "cases" / case_id
    case_dir.mkdir(parents=True, exist_ok=False)
    _write_json(case_dir / "result.json", result)
    return case_dir


def finish_run(run_dir: Path, status: str, invalid_reason: str | None = None) -> None:
    if status not in {"complete", "failed", "invalid"}:
        raise ValueError(f"invalid run status: {status!r}")
    manifest_path = Path(run_dir) / "manifest.json"
    manifest = _read_json(manifest_path)
    manifest["status"] = status
    manifest["finished_at"] = utc_now()
    manifest["invalid_reason"] = invalid_reason
    _write_json(manifest_path, manifest)


def median_wall_seconds(cases: Iterable[dict[str, Any]]) -> float:
    values = [float(case["wall_s"]) for case in cases if "wall_s" in case]
    if not values:
        raise ValueError("no complete wall_s cases")
    return float(statistics.median(values))


def _case_results(run_dir: Path) -> list[dict[str, Any]]:
    cases_dir = Path(run_dir) / "cases"
    results: list[dict[str, Any]] = []
    if not cases_dir.is_dir():
        return results
    for result_path in sorted(cases_dir.glob("*/result.json")):
        results.append(_read_json(result_path))
    return results


def summarize_runs(run_root: Path) -> dict[str, dict[str, Any]]:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for manifest_path in sorted(Path(run_root).glob("*/manifest.json")):
        manifest = _read_json(manifest_path)
        profile = manifest.get("profile")
        if profile not in PROFILES:
            continue
        grouped.setdefault(profile, []).extend(_case_results(manifest_path.parent))
    summary: dict[str, dict[str, Any]] = {}
    for profile, cases in grouped.items():
        complete = [case for case in cases if case.get("status") == "complete"]
        entry: dict[str, Any] = {"profile": profile, "cases": len(cases), "complete_cases": len(complete)}
        if complete:
            entry["median_wall_s"] = median_wall_seconds(complete)
        summary[profile] = entry
    return summary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-root", type=Path, default=ROOT / "build/nico-cpu20")
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--profile", required=True, choices=sorted(PROFILES))
    parser.add_argument("--artifact", action="append", default=[], metavar="NAME=PATH")
    args = parser.parse_args(argv)
    artifacts: dict[str, str | None] = {}
    for value in args.artifact:
        if "=" not in value:
            parser.error(f"--artifact must be NAME=PATH: {value!r}")
        name, path = value.split("=", 1)
        artifacts[name] = path
    try:
        run_dir = create_run(args.run_root, args.run_id, args.profile, artifacts=artifacts)
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    print(json.dumps({"run_dir": str(run_dir), "run_id": args.run_id, "profile": args.profile}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
