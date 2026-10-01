#!/usr/bin/env python3
"""Exercise the built NCT1 WGPU helper and its bounded stdout/readback path."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import struct
import subprocess
import sys
import tempfile
import time


FRAME_WIDTH = 33
FRAME_HEIGHT = 19
FRAME_COUNT = 3
FRAME_BYTES = FRAME_WIDTH * FRAME_HEIGHT * 4
TOTAL_RED_BYTES = FRAME_BYTES * FRAME_COUNT
PROGRESS_RE = re.compile(rb"NICO_PROGRESS ([1-3]) 3\r?\n")
DONE_RE = re.compile(rb"NICO_DONE 3\r?\n")
REQUIRED_BACKENDS = {
    "win32": {"dx12", "vulkan"},
    "darwin": {"metal"},
    "linux": {"vulkan"},
}


def blank_scene(width: int, height: int, frame_count: int, fps_num: int = 30, fps_den: int = 1) -> bytes:
    header = bytearray(80)
    header[0:4] = b"NCT1"
    struct.pack_into("<I", header, 4, 80)
    struct.pack_into("<IIIIII", header, 8, width, height, frame_count, fps_num, fps_den, 0)
    struct.pack_into("<I", header, 32, 0)  # draw count
    struct.pack_into("<32s", header, 36, bytes(32))
    struct.pack_into("<Q", header, 68, 0)  # total asset bytes
    struct.pack_into("<I", header, 76, 3)  # NCT1 flags
    return bytes(header)


def command(helper: Path, *args: str) -> list[str]:
    return [str(helper), *args]


def run_checked(args: list[str], *, input_bytes: bytes | None, timeout: float, label: str) -> subprocess.CompletedProcess[bytes]:
    try:
        result = subprocess.run(args, input=input_bytes, capture_output=True, timeout=timeout, check=False)
    except subprocess.TimeoutExpired as exc:
        raise AssertionError(f"{label} exceeded {timeout:.1f}s") from exc
    if result.returncode != 0:
        raise AssertionError(
            f"{label} failed with exit {result.returncode}: "
            f"stderr={result.stderr[-2048:]!r} stdout={result.stdout[-256:]!r}"
        )
    return result


def parse_self_test(helper: Path, backend: str, slots: int) -> dict[str, object]:
    result = run_checked(
        command(helper, "--self-test", "--backend", backend, "--readback-slots", str(slots)),
        input_bytes=None,
        timeout=12.0,
        label=f"self-test ring={slots}",
    )
    try:
        report = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise AssertionError(f"self-test stdout was not one JSON object: {result.stdout[:512]!r}") from exc
    expected = {
        "schema": 1,
        "protocol": "NCT1",
        "renderer": "wgpu",
        "readbackSlots": slots,
        "testFrameCount": FRAME_COUNT,
        "testFrameBytes": TOTAL_RED_BYTES,
    }
    for key, value in expected.items():
        if report.get(key) != value:
            raise AssertionError(f"self-test {key}={report.get(key)!r}, expected {value!r}")
    if report.get("backend") not in REQUIRED_BACKENDS.get(sys.platform, set()):
        raise AssertionError(f"unexpected or unsupported active backend: {report.get('backend')!r}")
    if not report.get("adapterName") or report.get("adapterType") not in {"DiscreteGpu", "IntegratedGpu"}:
        raise AssertionError(f"self-test did not report a hardware adapter: {report!r}")
    if not isinstance(report.get("maxTextureDimension2D"), int) or report["maxTextureDimension2D"] < FRAME_WIDTH:
        raise AssertionError(f"invalid adapter texture limit: {report!r}")
    if not re.fullmatch(r"[0-9a-f]{64}", str(report.get("testFrameSHA256", ""))):
        raise AssertionError(f"invalid self-test frame hash: {report!r}")
    return report


def verify_ring_output(helper: Path, backend: str, slots: int, fixture: bytes, expected_frame: bytes, report_dir: Path) -> tuple[bytes, dict[str, object]]:
    report_path = report_dir / f"runtime-{slots}.json"
    report_path.write_text('{"old":"report"}', encoding="utf-8")
    result = run_checked(
        command(
            helper,
            "--stdin",
            "--backend",
            backend,
            "--readback-slots",
            str(slots),
            "--report",
            str(report_path),
        ),
        input_bytes=fixture,
        timeout=30.0,
        label=f"NCT1 stream ring={slots}",
    )
    expected = expected_frame * FRAME_COUNT
    if result.stdout != expected:
        raise AssertionError(
            f"ring={slots} output length/hash mismatch: {len(result.stdout)} / "
            f"{hashlib.sha256(result.stdout).hexdigest()}, expected {len(expected)} / {hashlib.sha256(expected).hexdigest()}"
        )
    statuses = PROGRESS_RE.findall(result.stderr)
    if statuses != [b"1", b"2", b"3"] or not DONE_RE.search(result.stderr):
        raise AssertionError(f"ring={slots} status order is invalid: {result.stderr[-2048:]!r}")
    if result.stderr.find(b"NICO_DONE 3") < result.stderr.find(b"NICO_PROGRESS 3 3"):
        raise AssertionError("NICO_DONE appeared before the last completed frame")
    report_bytes = report_path.read_bytes()
    if len(report_bytes) > 64 * 1024:
        raise AssertionError(f"runtime report is oversized: {len(report_bytes)}")
    report = json.loads(report_bytes)
    expected_fields = {
        "schema": 1,
        "protocol": "NCT1",
        "renderer": "wgpu",
        "requestedBackend": backend,
        "readbackSlots": slots,
        "completedFrames": FRAME_COUNT,
    }
    for key, value in expected_fields.items():
        if report.get(key) != value:
            raise AssertionError(f"runtime report {key}={report.get(key)!r}, expected {value!r}")
    if report.get("adapterName") == "" or report.get("adapterType") not in {"DiscreteGpu", "IntegratedGpu"}:
        raise AssertionError(f"runtime report did not identify a hardware adapter: {report!r}")
    if list(report_dir.glob(f".runtime-{slots}.json.*.tmp")):
        raise AssertionError("atomic report temporary file was left behind")
    return result.stdout, report


def verify_bad_input(helper: Path, backend: str, fixture: bytes) -> None:
    for label, payload in (("truncated", fixture[:-1]), ("trailing", fixture + b"x")):
        result = subprocess.run(
            command(helper, "--stdin", "--backend", backend, "--readback-slots", "1"),
            input=payload,
            capture_output=True,
            timeout=5.0,
            check=False,
        )
        if result.returncode == 0 or result.stdout or b"NICO_DONE" in result.stderr:
            raise AssertionError(f"{label} NCT1 was not rejected before output: {result!r}")


def verify_broken_pipe(helper: Path, backend: str, fixture: bytes) -> None:
    proc = subprocess.Popen(
        command(helper, "--stdin", "--backend", backend, "--readback-slots", "1"),
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    try:
        proc.stdin.write(fixture)
        proc.stdin.close()
        proc.stdout.close()
        try:
            proc.wait(timeout=5.0)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5.0)
            raise AssertionError("broken-pipe helper did not stop within 5s")
        stderr = proc.stderr.read()
        if proc.returncode == 0 or b"NICO_DONE" in stderr:
            raise AssertionError(f"broken pipe was reported as success: exit={proc.returncode} stderr={stderr[-1024:]!r}")
    finally:
        for stream in (proc.stdin, proc.stdout, proc.stderr):
            if stream and not stream.closed:
                stream.close()
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=5.0)


def verify_backpressure_cancel(helper: Path, backend: str, report_dir: Path) -> None:
    # A large blank frame stream exceeds any OS pipe buffer immediately while
    # keeping GPU memory bounded to the target plus three readback slots.
    payload = blank_scene(1920, 1080, 30)
    report_path = report_dir / "backpressure.json"
    proc = subprocess.Popen(
        command(helper, "--stdin", "--backend", backend, "--readback-slots", "3", "--report", str(report_path)),
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    try:
        proc.stdin.write(payload)
        proc.stdin.close()
        deadline = time.monotonic() + 12.0
        while not report_path.exists() and time.monotonic() < deadline and proc.poll() is None:
            time.sleep(0.05)
        if not report_path.is_file():
            raise AssertionError("helper did not publish its initial capability report")
        initial_report = json.loads(report_path.read_bytes())
        if initial_report.get("completedFrames") != 0 or initial_report.get("readbackSlots") != 3:
            raise AssertionError(f"initial report has unexpected state: {initial_report!r}")
        time.sleep(1.5)
        if proc.poll() is not None:
            stderr = proc.stderr.read()
            raise AssertionError(f"back-pressured helper exited early: {proc.returncode} {stderr[-1024:]!r}")
        proc.kill()
        try:
            proc.wait(timeout=5.0)
        except subprocess.TimeoutExpired as exc:
            raise AssertionError("canceled back-pressured helper did not exit within 5s") from exc
        stderr = proc.stderr.read()
        if b"NICO_DONE" in stderr:
            raise AssertionError("back-pressured helper reported completion after cancellation")
    finally:
        for stream in (proc.stdin, proc.stdout, proc.stderr):
            if stream and not stream.closed:
                stream.close()
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=5.0)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--helper", required=True, type=Path)
    parser.add_argument("--backend", choices=("auto", "dx12", "vulkan", "metal"), default="auto")
    args = parser.parse_args()
    helper = args.helper.resolve()
    if not helper.is_file():
        parser.error(f"helper does not exist: {helper}")
    if sys.platform not in REQUIRED_BACKENDS:
        parser.error(f"unsupported test host platform: {sys.platform}")

    repository_root = Path(__file__).resolve().parents[1]
    fixture_path = repository_root / "internal" / "nicorender" / "testdata" / "timeline" / "wire" / "red-33x19-3frames.nct"
    fixture = fixture_path.read_bytes()
    expected_frame = bytes([255, 0, 0, 255]) * FRAME_WIDTH * FRAME_HEIGHT
    with tempfile.TemporaryDirectory(prefix="nico-timeline-compositor-") as temp:
        report_dir = Path(temp)
        active_backends = []
        outputs = []
        for slots in (1, 2, 3):
            self_test = parse_self_test(helper, args.backend, slots)
            active_backends.append(self_test["backend"])
            output, _report = verify_ring_output(helper, args.backend, slots, fixture, expected_frame, report_dir)
            outputs.append(output)
        if len(set(active_backends)) != 1 or outputs[0] != outputs[1] or outputs[1] != outputs[2]:
            raise AssertionError("ring 1/2/3 did not use one backend and produce identical RGBA")
        active_backend = str(active_backends[0])
        verify_bad_input(helper, active_backend, fixture)
        verify_broken_pipe(helper, active_backend, fixture)
        verify_backpressure_cancel(helper, active_backend, report_dir)

    print(json.dumps({
        "result": "PASS",
        "helper": str(helper),
        "backend": active_backends[0],
        "readbackSlots": [1, 2, 3],
        "framesPerSlot": FRAME_COUNT,
        "outputBytes": TOTAL_RED_BYTES,
        "failureCases": ["truncated", "trailing", "broken-pipe", "backpressure-cancel"],
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # noqa: BLE001 - render a concise harness failure
        print(f"FAIL: {exc}", file=sys.stderr)
        raise
