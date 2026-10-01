"""Machine-readable result contract for the isolated D3D11/NVENC probe."""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Mapping


SCHEMA_VERSION = 1
EXPERIMENT = "nico-gpu-nvenc"
STATUSES = frozenset({"unavailable", "rejected", "failed", "passed"})


class ManifestError(ValueError):
    """Raised when a result would make an unverifiable claim."""


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _default_counters() -> dict[str, Any]:
    return {
        "pool_capacity": 0,
        "submitted_before_eos": 0,
        "accepted_frames": 0,
        "texture_registrations": 0,
        "eos_sent": False,
        "eos_completed": False,
    }


def build_result(
    status: str,
    *,
    reason: str | None = None,
    checks: Mapping[str, Any] | None = None,
    counters: Mapping[str, Any] | None = None,
    toolchain: Mapping[str, Any] | None = None,
    commands: list[list[str]] | None = None,
    metadata: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Build a result without turning missing measurements into PASS."""
    if status not in STATUSES:
        raise ManifestError(f"unknown status: {status}")

    merged_checks = dict(checks or {})
    merged_counters = _default_counters()
    merged_counters.update(counters or {})
    pool_capacity = int(merged_counters["pool_capacity"] or 0)
    submitted = int(merged_counters["submitted_before_eos"] or 0)
    eos_sent = bool(merged_counters["eos_sent"])
    overflow = pool_capacity > 0 and submitted >= pool_capacity * 4 and eos_sent
    merged_counters["pool_capacity"] = pool_capacity
    merged_counters["submitted_before_eos"] = submitted
    merged_counters["eos_sent"] = eos_sent
    merged_counters["eos_completed"] = bool(merged_counters["eos_completed"])

    result: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "experiment": EXPERIMENT,
        "created_at": _utc_now(),
        "status": status,
        "reason": reason,
        "unavailable_reasons": list((metadata or {}).get("unavailable_reasons", [])),
        "checks": merged_checks,
        "gates": {
            "d3d11_texture_registration": bool(
                merged_checks.get("d3d11_texture_registration", False)
            ),
            "nvenc_acceptance": bool(merged_checks.get("nvenc_acceptance", False)),
            "pool_progress": bool(merged_checks.get("pool_progress", False)),
            "pool_capacity_exceeded_before_eos": overflow,
        },
        "counters": merged_counters,
        "toolchain": dict(toolchain or {}),
        "commands": commands or [],
        "metadata": dict(metadata or {}),
    }
    return result


def validate_result(result: Mapping[str, Any]) -> list[str]:
    """Return contract violations; callers decide whether to raise or report."""
    errors: list[str] = []
    if result.get("schema_version") != SCHEMA_VERSION:
        errors.append("schema_version")
    if result.get("experiment") != EXPERIMENT:
        errors.append("experiment")
    status = result.get("status")
    if status not in STATUSES:
        errors.append("status")
    if status in {"unavailable", "rejected", "failed"} and not str(result.get("reason") or "").strip():
        errors.append("reason")

    counters = result.get("counters")
    gates = result.get("gates")
    if not isinstance(counters, Mapping):
        errors.append("counters")
        counters = {}
    if not isinstance(gates, Mapping):
        errors.append("gates")
        gates = {}

    try:
        capacity = int(counters.get("pool_capacity", 0))
        submitted = int(counters.get("submitted_before_eos", 0))
    except (TypeError, ValueError):
        capacity, submitted = 0, 0
        errors.append("counter_types")
    if capacity < 0 or submitted < 0:
        errors.append("counter_range")
    expected_overflow = capacity > 0 and submitted >= capacity * 4 and bool(counters.get("eos_sent"))
    if bool(gates.get("pool_capacity_exceeded_before_eos")) != expected_overflow:
        errors.append("pool_capacity_exceeded_before_eos")

    if status == "passed":
        for gate in (
            "d3d11_texture_registration",
            "nvenc_acceptance",
            "pool_progress",
            "pool_capacity_exceeded_before_eos",
        ):
            if gates.get(gate) is not True:
                errors.append(gate)
        if not bool(counters.get("eos_sent")):
            errors.append("eos_sent")
        if not bool(counters.get("eos_completed")):
            errors.append("eos_completed")
    elif bool(gates.get("pool_capacity_exceeded_before_eos")):
        errors.append("non_passed_pool_gate")
    return errors


def write_result(path: Path, result: Mapping[str, Any]) -> None:
    errors = validate_result(result)
    if errors:
        raise ManifestError("invalid result: " + ", ".join(errors))
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)
