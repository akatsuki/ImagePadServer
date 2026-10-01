"""Build and run explicitly capability-checked FFmpeg diagnostic commands."""

from __future__ import annotations

import json
import os
import re
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable


SCHEMA_VERSION = 1
_OPTION_RE = re.compile(r"(?m)^\s*(-benchmark(?:_all)?)\b")
_MAX_CAPTURE_BYTES = 1024 * 1024


class DiagnosticError(RuntimeError):
    pass


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def probe_options(ffmpeg: str) -> set[str]:
    """Return benchmark flags advertised by this exact FFmpeg executable."""

    try:
        completed = subprocess.run(
            [ffmpeg, "-hide_banner", "-h", "full"],
            capture_output=True,
            check=False,
            timeout=30,
        )
    except OSError as exc:
        raise DiagnosticError(f"cannot execute ffmpeg help: {exc}") from exc
    if completed.returncode != 0:
        detail = completed.stderr[-4096:].decode("utf-8", errors="replace")
        raise DiagnosticError(f"ffmpeg help failed with {completed.returncode}: {detail}")
    text = (completed.stdout + completed.stderr).decode("utf-8", errors="replace")
    return set(_OPTION_RE.findall(text))


def build_diagnostic_args(
    base_args: Iterable[str],
    advertised_options: set[str],
    *,
    benchmark_all: bool = False,
    verbose_filters: bool = False,
) -> list[str]:
    """Add only help-confirmed diagnostic flags to a separate FFmpeg run."""

    required = "-benchmark_all" if benchmark_all else "-benchmark"
    if required not in advertised_options:
        raise DiagnosticError(f"FFmpeg does not advertise required option {required}")
    args = list(base_args)
    args.append(required)
    if verbose_filters:
        args.extend(["-loglevel", "verbose"])
    return args


def _bounded_text(data: bytes) -> str:
    if len(data) <= _MAX_CAPTURE_BYTES:
        return data.decode("utf-8", errors="replace")
    return data[-_MAX_CAPTURE_BYTES:].decode("utf-8", errors="replace")


def run_diagnostic(
    ffmpeg: str,
    base_args: Iterable[str],
    record_path: str | os.PathLike[str],
    *,
    benchmark_all: bool = False,
    verbose_filters: bool = False,
    timeout_seconds: float = 900,
) -> dict:
    """Run one capability-checked diagnostic and atomically save its record."""

    advertised = probe_options(ffmpeg)
    args = build_diagnostic_args(
        base_args,
        advertised,
        benchmark_all=benchmark_all,
        verbose_filters=verbose_filters,
    )
    started = _utc_now()
    try:
        completed = subprocess.run(
            [ffmpeg, *args],
            capture_output=True,
            check=False,
            timeout=timeout_seconds,
        )
        timed_out = False
        stdout = completed.stdout
        stderr = completed.stderr
        return_code = completed.returncode
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        return_code = None
    record = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started,
        "finished_at": _utc_now(),
        "ffmpeg": os.fspath(ffmpeg),
        "argv": args,
        "advertised_options": sorted(advertised),
        "benchmark_all": benchmark_all,
        "verbose_filters": verbose_filters,
        "timeout_seconds": timeout_seconds,
        "timed_out": timed_out,
        "return_code": return_code,
        "stdout_tail": _bounded_text(stdout),
        "stderr_tail": _bounded_text(stderr),
    }
    destination = Path(record_path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=destination.parent, delete=False) as stream:
        temporary = Path(stream.name)
        json.dump(record, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    try:
        os.replace(temporary, destination)
    except Exception:
        temporary.unlink(missing_ok=True)
        raise
    return record
