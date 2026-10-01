#!/usr/bin/env python3
"""Create the target manifest for a built NCT1 WGPU compositor."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import threading
from pathlib import Path
from types import SimpleNamespace
from typing import Mapping

WGPU_VERSION = "0.20.1"
HELPER_PROBE_TIMEOUT_SECONDS = 10.0
HELPER_PROBE_OUTPUT_LIMIT_BYTES = 64 * 1024
NCT2_CAPABILITY_ARGS = ["--capabilities", "--protocol-version", "2"]
NCT1_SELF_TEST_ARGS = ["--self-test", "--backend", "auto", "--readback-slots", "3"]
NCT2_REQUIRED_CAPABILITIES = frozenset(
    {
        "incremental-assets",
        "ordered-elements",
        "watermarks",
        "end-and-eof",
    }
)
NCT2_CAPABILITY_FIELDS = frozenset(
    {
        "schema",
        "protocol",
        "protocolVersion",
        "inputMode",
        "outputFormat",
        "capabilities",
    }
)
NCT1_SELF_TEST_FIELDS = frozenset(
    {
        "schema",
        "protocol",
        "renderer",
        "version",
        "backend",
        "adapterName",
        "adapterType",
        "readbackSlots",
        "maxTextureDimension2D",
        "testFrameCount",
        "testFrameBytes",
        "testFrameSHA256",
    }
)
NCT1_SELF_TEST_OPTIONAL_FIELDS = frozenset(
    {
        "requestedAssetLayout",
        "assetLayout",
        "assetLayoutFallbackReason",
        "assetPageCount",
        "assetSourceBytes",
        "assetAllocatedBytes",
    }
)
TARGETS = {
    "x86_64-pc-windows-msvc": ("windows", "amd64"),
    "x86_64-pc-windows-gnu": ("windows", "amd64"),
    "aarch64-pc-windows-msvc": ("windows", "arm64"),
    "x86_64-apple-darwin": ("darwin", "amd64"),
    "aarch64-apple-darwin": ("darwin", "arm64"),
    "x86_64-unknown-linux-gnu": ("linux", "amd64"),
    "aarch64-unknown-linux-gnu": ("linux", "arm64"),
    "x86_64-unknown-linux-musl": ("linux", "amd64"),
    "aarch64-unknown-linux-musl": ("linux", "arm64"),
}
BUILD_MANIFEST_MODES = frozenset(
    {
        "release",
        "release-thin",
        "release-thin-one",
        "release-pgo",
        "release-thin-pgo",
        "release-thin-one-pgo",
    }
)
BUILD_ENV_OVERRIDE_EXACT = frozenset(
    {
        "CARGO_BUILD_TARGET",
        "CARGO_ENCODED_RUSTFLAGS",
        "CARGO_HOME",
        "CARGO_TARGET_DIR",
        "RUSTC",
        "RUSTC_WORKSPACE_WRAPPER",
        "RUSTC_WRAPPER",
        "RUSTFLAGS",
        "RUSTUP_HOME",
        "RUSTUP_TOOLCHAIN",
    }
)
BUILD_ENV_PRESERVED = frozenset({"CARGO_HOME", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN"})
_ANSI_ESCAPE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


def _strict_json_object(payload: bytes, label: str) -> dict[str, object]:
    if not payload or len(payload) > HELPER_PROBE_OUTPUT_LIMIT_BYTES:
        raise ValueError(
            f"{label} size must be between 1 and {HELPER_PROBE_OUTPUT_LIMIT_BYTES} bytes"
        )

    def reject_duplicate_fields(pairs):
        value = {}
        for key, field in pairs:
            if key in value:
                raise ValueError(f"{label} has duplicate field {key!r}")
            value[key] = field
        return value

    try:
        decoded = payload.decode("utf-8", errors="strict")
        value = json.loads(decoded, object_pairs_hook=reject_duplicate_fields)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError(f"{label} is not valid UTF-8 JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be a JSON object")
    return value


def _validate_nct2_capabilities(payload: bytes) -> dict[str, object]:
    value = _strict_json_object(payload, "NCT2 capability response")
    fields = frozenset(value)
    if fields != NCT2_CAPABILITY_FIELDS:
        missing = sorted(NCT2_CAPABILITY_FIELDS - fields)
        unknown = sorted(fields - NCT2_CAPABILITY_FIELDS)
        raise ValueError(
            f"NCT2 capability fields mismatch (missing={missing}, unknown={unknown})"
        )
    if type(value["schema"]) is not int or value["schema"] != 1:
        raise ValueError("NCT2 capability schema must be integer 1")
    if value["protocol"] != "NCT2":
        raise ValueError("helper capability protocol must be NCT2")
    if type(value["protocolVersion"]) is not int or value["protocolVersion"] != 2:
        raise ValueError("NCT2 protocolVersion must be integer 2")
    if value["inputMode"] != "--stdin-stream":
        raise ValueError("NCT2 inputMode must be --stdin-stream")
    if value["outputFormat"] != "rgba8":
        raise ValueError("NCT2 outputFormat must be rgba8")
    capabilities = value["capabilities"]
    if not isinstance(capabilities, list) or any(
        not isinstance(item, str) for item in capabilities
    ):
        raise ValueError("NCT2 capabilities must be an array of strings")
    missing_capabilities = sorted(NCT2_REQUIRED_CAPABILITIES - set(capabilities))
    if missing_capabilities:
        raise ValueError(
            f"NCT2 capabilities missing required values: {missing_capabilities}"
        )
    return value


def _validate_nct1_self_test(payload: bytes, goos: str) -> None:
    value = _strict_json_object(payload, "NCT1 self-test response")
    fields = frozenset(value)
    missing = sorted(NCT1_SELF_TEST_FIELDS - fields)
    unknown = sorted(fields - NCT1_SELF_TEST_FIELDS - NCT1_SELF_TEST_OPTIONAL_FIELDS)
    if missing or unknown:
        raise ValueError(
            f"NCT1 self-test fields mismatch (missing={missing}, unknown={unknown})"
        )
    if type(value["schema"]) is not int or value["schema"] != 1:
        raise ValueError("NCT1 self-test schema must be integer 1")
    if value["protocol"] != "NCT1" or value["renderer"] != "wgpu":
        raise ValueError(
            "legacy helper did not report the NCT1 wgpu self-test contract"
        )
    if not isinstance(value["version"], str) or not value["version"].strip():
        raise ValueError("NCT1 self-test version is missing")
    if not isinstance(value["adapterName"], str) or not value["adapterName"].strip():
        raise ValueError("NCT1 self-test adapterName is missing")
    if not isinstance(value["adapterType"], str) or value["adapterType"] not in {
        "DiscreteGpu",
        "IntegratedGpu",
    }:
        raise ValueError("NCT1 self-test did not report a supported hardware adapter")
    allowed_backends = {
        "windows": {"dx12", "vulkan"},
        "darwin": {"metal"},
        "linux": {"vulkan"},
    }
    if not isinstance(value["backend"], str) or value[
        "backend"
    ] not in allowed_backends.get(goos, set()):
        raise ValueError(
            f"NCT1 self-test backend {value['backend']!r} is not allowed on {goos}"
        )
    if type(value["readbackSlots"]) is not int or value["readbackSlots"] != 3:
        raise ValueError(
            "NCT1 self-test did not use the requested three readback slots"
        )
    if (
        type(value["maxTextureDimension2D"]) is not int
        or value["maxTextureDimension2D"] < 33
    ):
        raise ValueError("NCT1 self-test texture limit is too small")
    if type(value["testFrameCount"]) is not int or value["testFrameCount"] != 3:
        raise ValueError("NCT1 self-test did not read back three frames")
    if type(value["testFrameBytes"]) is not int or value["testFrameBytes"] != 7524:
        raise ValueError("NCT1 self-test readback byte count is invalid")
    if (
        not isinstance(value["testFrameSHA256"], str)
        or len(value["testFrameSHA256"]) != 64
    ):
        raise ValueError("NCT1 self-test frame hash is invalid")
    try:
        frame_digest = bytes.fromhex(value["testFrameSHA256"])
    except ValueError as exc:
        raise ValueError("NCT1 self-test frame hash is invalid") from exc
    if len(frame_digest) != 32:
        raise ValueError("NCT1 self-test frame hash is invalid")
    for field in ("requestedAssetLayout", "assetLayout"):
        if field in value and not isinstance(value[field], str):
            raise ValueError(f"NCT1 self-test {field} must be a string")
    fallback = value.get("assetLayoutFallbackReason", "")
    if fallback is not None and not isinstance(fallback, str):
        raise ValueError("NCT1 self-test assetLayoutFallbackReason must be a string")
    requested = value.get("requestedAssetLayout", "").strip()
    actual = value.get("assetLayout", "").strip()
    if requested or actual:
        if requested != "separate":
            raise ValueError("NCT1 self-test requested an unexpected asset layout")
        if actual not in {"separate", "atlas"}:
            raise ValueError("NCT1 self-test reported an invalid actual asset layout")
        if actual != "separate":
            raise ValueError("NCT1 self-test did not honor separate asset layout")
        if fallback:
            raise ValueError(
                "NCT1 self-test reported an unexpected asset layout fallback"
            )
        layout_fields = {"assetPageCount", "assetSourceBytes", "assetAllocatedBytes"}
        if layout_fields - fields:
            raise ValueError(
                f"NCT1 self-test is missing asset layout fields: {sorted(layout_fields - fields)}"
            )
        for field in ("assetPageCount", "assetSourceBytes", "assetAllocatedBytes"):
            if type(value[field]) is not int or value[field] < 0:
                raise ValueError(f"NCT1 self-test {field} is invalid")
        if value["assetAllocatedBytes"] != value["assetSourceBytes"]:
            raise ValueError(
                "NCT1 separate asset allocation does not match source bytes"
            )
        if value["assetPageCount"] == 0 and (
            value["assetSourceBytes"] or value["assetAllocatedBytes"]
        ):
            raise ValueError(
                "NCT1 self-test reported asset bytes without texture pages"
            )


def _bounded_read(
    stream, process: subprocess.Popen[bytes], limit: int, overflow: threading.Event
) -> bytes:
    collected = bytearray()
    while True:
        chunk = stream.read(8192)
        if not chunk:
            break
        remaining = limit + 1 - len(collected)
        if remaining > 0:
            collected.extend(chunk[:remaining])
        if len(collected) > limit and not overflow.is_set():
            overflow.set()
            try:
                process.kill()
            except OSError:
                pass
    return bytes(collected)


def run_helper_bounded(
    binary_path: Path,
    args: list[str],
    *,
    timeout_seconds: float,
    output_limit_bytes: int,
) -> SimpleNamespace:
    command = [str(binary_path), *args]
    creationflags = getattr(subprocess, "CREATE_NO_WINDOW", 0)
    try:
        process = subprocess.Popen(
            command,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            creationflags=creationflags,
        )
    except OSError as exc:
        raise ValueError(f"failed to start helper capability probe: {exc}") from exc

    stdout_overflow = threading.Event()
    stderr_overflow = threading.Event()
    stdout_data: list[bytes] = []
    stderr_data: list[bytes] = []
    stdout_thread = threading.Thread(
        target=lambda: stdout_data.append(
            _bounded_read(process.stdout, process, output_limit_bytes, stdout_overflow)
        ),
        daemon=True,
    )
    stderr_thread = threading.Thread(
        target=lambda: stderr_data.append(
            _bounded_read(process.stderr, process, output_limit_bytes, stderr_overflow)
        ),
        daemon=True,
    )
    stdout_thread.start()
    stderr_thread.start()
    timed_out = False
    try:
        returncode = process.wait(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        timed_out = True
        process.kill()
        returncode = process.wait()
    stdout_thread.join(timeout=2.0)
    stderr_thread.join(timeout=2.0)
    reader_stuck = stdout_thread.is_alive() or stderr_thread.is_alive()
    if not stdout_thread.is_alive() and process.stdout is not None:
        process.stdout.close()
    if not stderr_thread.is_alive() and process.stderr is not None:
        process.stderr.close()
    return SimpleNamespace(
        returncode=returncode,
        stdout=stdout_data[0] if stdout_data else b"",
        stderr=stderr_data[0] if stderr_data else b"",
        timed_out=timed_out,
        stdout_exceeded=stdout_overflow.is_set(),
        stderr_exceeded=stderr_overflow.is_set(),
        reader_stuck=reader_stuck,
    )


def _run_probe(binary_path: Path, args: list[str], runner) -> SimpleNamespace:
    try:
        result = runner(
            binary_path,
            args,
            timeout_seconds=HELPER_PROBE_TIMEOUT_SECONDS,
            output_limit_bytes=HELPER_PROBE_OUTPUT_LIMIT_BYTES,
        )
    except subprocess.TimeoutExpired as exc:
        raise ValueError("helper probe timed out") from exc
    if getattr(result, "reader_stuck", False):
        raise ValueError("helper probe output reader did not stop")
    if getattr(result, "timed_out", False):
        raise ValueError("helper probe timed out")
    return result


def inspect_helper_protocol(
    binary_path: Path, runner=run_helper_bounded, *, goos: str
) -> dict[str, object]:
    capability = _run_probe(binary_path, NCT2_CAPABILITY_ARGS, runner)
    if capability.returncode == 0:
        if getattr(capability, "stdout_exceeded", False) or getattr(
            capability, "stderr_exceeded", False
        ):
            raise ValueError("helper capability response exceeded the output limit")
        return _validate_nct2_capabilities(capability.stdout)

    unsupported = (
        capability.returncode is not None
        and capability.returncode != 0
        and not getattr(capability, "stdout_exceeded", False)
        and not getattr(capability, "stderr_exceeded", False)
        and capability.stdout == b""
        and capability.stderr.strip() == b'NICO_ERROR unknown argument "--capabilities"'
    )
    if not unsupported:
        raise ValueError(
            f"helper capability probe failed (exit {capability.returncode}): {capability.stderr.decode('utf-8', errors='replace').strip()}"
        )

    legacy = _run_probe(binary_path, NCT1_SELF_TEST_ARGS, runner)
    if legacy.returncode != 0:
        raise ValueError(
            f"legacy NCT1 self-test failed (exit {legacy.returncode}): {legacy.stderr.decode('utf-8', errors='replace').strip()}"
        )
    if getattr(legacy, "stdout_exceeded", False) or getattr(
        legacy, "stderr_exceeded", False
    ):
        raise ValueError("legacy NCT1 self-test exceeded the output limit")
    _validate_nct1_self_test(legacy.stdout, goos)
    return {"schema": 1, "protocol": "NCT1"}


def create_manifest(binary: bytes, target: str) -> dict[str, object]:
    try:
        operating_system, architecture = TARGETS[target]
    except KeyError as exc:
        raise ValueError(f"unsupported Rust target: {target}") from exc
    if not binary:
        raise ValueError("compositor binary is empty")
    return {
        "schema": 1,
        "protocol": "NCT1",
        "os": operating_system,
        "architecture": architecture,
        "sha256": hashlib.sha256(binary).hexdigest(),
        "wgpuVersion": WGPU_VERSION,
        "cargoTarget": target,
    }


def _require_sha256(value: object, label: str) -> str:
    if (
        not isinstance(value, str)
        or len(value) != 64
        or any(character not in "0123456789abcdef" for character in value)
    ):
        raise ValueError(f"{label} must be a lowercase SHA-256 hex digest")
    return value


def _canonical_source_sha256(source_inputs: list[dict[str, str]]) -> str:
    payload = "".join(
        f"{item['path']}\0{item['sha256']}\n"
        for item in sorted(source_inputs, key=lambda value: value["path"])
    ).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def collect_build_source_inputs(source_root: Path) -> list[dict[str, str]]:
    """Hash the compositor sources, build scripts, toolchain selectors, and Cargo config."""
    root = source_root.resolve()
    crate_root = root / "gpu" / "nico-compositord"
    if not crate_root.is_dir():
        raise ValueError(f"compositor source directory is missing: {crate_root}")

    candidates: dict[str, Path] = {}
    for path in crate_root.rglob("*"):
        if path.is_symlink():
            raise ValueError(f"build source input is a symlink: {path}")
        if not path.is_file():
            continue
        relative = path.relative_to(root).as_posix()
        if "target" in Path(relative).parts or ".git" in Path(relative).parts:
            continue
        candidates[relative] = path

    for relative in (
        "scripts/nico_timeline_manifest.py",
        "scripts/experiments/nico-timeline/build-variants.ps1",
    ):
        path = root / relative
        if not path.is_file():
            raise ValueError(f"build source input is missing: {path}")
        candidates[relative] = path

    ancestors = [root, *root.parents]
    for directory in ancestors:
        for name in ("rust-toolchain", "rust-toolchain.toml"):
            path = directory / name
            if path.is_file():
                label = path.relative_to(root).as_posix() if path.is_relative_to(root) else f"ancestor/{directory.name}/{name}"
                candidates[label] = path
        for name in (".cargo/config", ".cargo/config.toml"):
            path = directory / name
            if path.is_file():
                label = path.relative_to(root).as_posix() if path.is_relative_to(root) else f"ancestor/{directory.name}/{name}"
                candidates[label] = path

    cargo_home = Path(os.environ.get("CARGO_HOME") or (Path.home() / ".cargo"))
    for name in ("config", "config.toml"):
        path = cargo_home / name
        if path.is_file():
            candidates[f"cargo-home/{name}"] = path

    records = []
    for label, path in sorted(candidates.items()):
        records.append(
            {
                "path": label,
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            }
        )
    if not any(item["path"] == "gpu/nico-compositord/Cargo.lock" for item in records):
        raise ValueError("compositor Cargo.lock is missing from build inputs")
    return records


def collect_build_environment_overrides(
    environment: Mapping[str, str],
) -> list[dict[str, str]]:
    """Hash build-affecting inherited environment values without exposing them."""
    records = []
    for name, value in sorted(environment.items()):
        upper_name = name.upper()
        is_profile = upper_name.startswith("CARGO_PROFILE_")
        is_target_rustflags = (
            upper_name.startswith("CARGO_TARGET_")
            and upper_name.endswith("_RUSTFLAGS")
        )
        if not (
            upper_name in BUILD_ENV_OVERRIDE_EXACT
            or is_profile
            or is_target_rustflags
        ):
            continue
        records.append(
            {
                "name": name,
                "value_sha256": hashlib.sha256(value.encode("utf-8")).hexdigest(),
                "action": "preserved" if upper_name in BUILD_ENV_PRESERVED else "scrubbed",
            }
        )
    return records


def parse_cargo_rustc_invocations(verbose_log: str) -> list[str]:
    """Return Cargo's actual rustc command lines from a fresh verbose build."""
    invocations = []
    for raw_line in verbose_log.splitlines():
        line = _ANSI_ESCAPE.sub("", raw_line).strip()
        marker = "Running `"
        start = line.find(marker)
        if start < 0:
            continue
        command_start = start + len(marker)
        command_end = line.rfind("`")
        if command_end < command_start:
            continue
        command = line[command_start:command_end]
        # Windows Cargo wraps rustc in `set KEY=VALUE&& ... && rustc.exe ...`.
        compiler_command = command.rsplit("&&", 1)[-1].strip()
        executable_match = re.match(
            r'(?i)(?P<exe>"[^"`]*rustc(?:\.exe)?"|[^\s`]*rustc(?:\.exe)?)(?=\s|$)',
            compiler_command,
        )
        if executable_match is None:
            continue
        executable = executable_match.group("exe").strip('"')
        if Path(executable).name.lower() not in {
            "rustc",
            "rustc.exe",
        }:
            continue
        invocations.append(f"Running `{compiler_command}`")
    if not invocations:
        raise ValueError("Cargo verbose log contains no rustc compiler invocations")
    return invocations


def create_build_manifest(
    binary: bytes,
    *,
    build_mode: str,
    target: str,
    source_inputs: list[dict[str, str]],
    cargo_lock_sha256: str,
    rustc_verbose: str,
    cargo_verbose: str,
    effective_compiler_invocations: list[str],
    environment_overrides: list[dict[str, str]],
    training_manifest_sha256: str | None = None,
    profile_sha256: str | None = None,
) -> dict[str, object]:
    """Create experiment-only provenance, separate from the strict runtime manifest."""
    if not isinstance(build_mode, str) or build_mode not in BUILD_MANIFEST_MODES:
        raise ValueError(f"unsupported build mode: {build_mode!r}")
    if not isinstance(target, str) or target not in TARGETS:
        raise ValueError(f"unsupported Rust target: {target}")
    if not binary:
        raise ValueError("compositor binary is empty")
    if not isinstance(source_inputs, list) or not source_inputs:
        raise ValueError("source inputs must be a non-empty array")
    checked_sources = []
    seen_paths = set()
    for item in source_inputs:
        if not isinstance(item, dict) or set(item) != {"path", "sha256"}:
            raise ValueError("source input fields are invalid")
        path = item["path"]
        if not isinstance(path, str) or not path.strip() or path in seen_paths:
            raise ValueError("source input path must be non-empty and unique")
        seen_paths.add(path)
        checked_sources.append(
            {"path": path, "sha256": _require_sha256(item["sha256"], "source input SHA-256")}
        )
    source_sha256 = _canonical_source_sha256(checked_sources)
    cargo_lock_sha256 = _require_sha256(cargo_lock_sha256, "Cargo.lock SHA-256")
    lock_inputs = [
        item
        for item in checked_sources
        if item["path"] == "gpu/nico-compositord/Cargo.lock"
    ]
    if len(lock_inputs) != 1 or lock_inputs[0]["sha256"] != cargo_lock_sha256:
        raise ValueError("Cargo.lock hash does not match the build source inputs")
    if not isinstance(rustc_verbose, str) or not rustc_verbose.strip():
        raise ValueError("rustc toolchain record must be non-empty")
    if not isinstance(cargo_verbose, str) or not cargo_verbose.strip():
        raise ValueError("Cargo toolchain record must be non-empty")
    if not isinstance(effective_compiler_invocations, list) or not all(
        isinstance(item, str) and item.strip()
        for item in effective_compiler_invocations
    ):
        raise ValueError("effective compiler invocations must be a non-empty string array")
    if not effective_compiler_invocations:
        raise ValueError("effective compiler invocations must be a non-empty string array")

    pgo_build = build_mode.endswith("-pgo")
    if pgo_build:
        training_manifest_sha256 = _require_sha256(
            training_manifest_sha256, "training manifest SHA-256"
        )
        profile_sha256 = _require_sha256(profile_sha256, "PGO profile SHA-256")
        use_pattern = re.compile(r"(?i)(?:^|\s)-C\s*profile-use(?:=|\s+)")
        generate_pattern = re.compile(r"(?i)(?:^|\s)-C\s*profile-generate(?:=|\s+)")
        if any(generate_pattern.search(item) for item in effective_compiler_invocations):
            raise ValueError("profile-use build must not contain profile-generate compiler flags")
        compositor_invocations = [
            item
            for item in effective_compiler_invocations
            if "--crate-name nico_compositord" in item
        ]
        if not compositor_invocations or not any(
            use_pattern.search(item) for item in compositor_invocations
        ):
            raise ValueError("profile-use compiler flag is missing from the compositor invocation")
    elif training_manifest_sha256 is not None or profile_sha256 is not None:
        raise ValueError("training manifest and PGO profile hashes are only valid for a PGO build")

    overrides = []
    if not isinstance(environment_overrides, list):
        raise ValueError("build environment overrides must be an array")
    for item in environment_overrides:
        if not isinstance(item, dict) or set(item) != {
            "name",
            "value_sha256",
            "action",
        }:
            raise ValueError("build environment override fields are invalid")
        name = item["name"]
        if not isinstance(name, str) or not name.strip():
            raise ValueError("build environment override name must be non-empty")
        digest = _require_sha256(item["value_sha256"], "environment value SHA-256")
        action = item["action"]
        if not isinstance(action, str) or action not in {"scrubbed", "preserved"}:
            raise ValueError("build environment override action is invalid")
        expected_action = "preserved" if name.upper() in BUILD_ENV_PRESERVED else "scrubbed"
        if action != expected_action:
            raise ValueError(f"incorrect build environment action for {name}")
        if name.upper() in {item["name"].upper() for item in overrides}:
            raise ValueError(f"duplicate build environment override: {name}")
        overrides.append({"name": name, "valueSha256": digest, "action": action})

    invocations = list(effective_compiler_invocations)
    invocation_bytes = "\n".join(invocations).encode("utf-8")
    manifest = {
        "schema": 2 if pgo_build else 1,
        "kind": "nico-compositor-build",
        "buildMode": build_mode,
        "target": target,
        "sourceSha256": source_sha256,
        "sourceInputs": sorted(checked_sources, key=lambda item: item["path"]),
        "cargoLockSha256": cargo_lock_sha256,
        "rustcVerbose": rustc_verbose,
        "cargoVerbose": cargo_verbose,
        "effectiveCompilerInvocations": invocations,
        "effectiveCompilerInvocationsSha256": hashlib.sha256(
            invocation_bytes
        ).hexdigest(),
        "environmentOverrides": overrides,
        "binarySha256": hashlib.sha256(binary).hexdigest(),
        "binarySizeBytes": len(binary),
    }
    if pgo_build:
        manifest["trainingManifestSha256"] = training_manifest_sha256
        manifest["profileSha256"] = profile_sha256
    return manifest


def validate_build_manifest(manifest: dict[str, object], binary: bytes) -> None:
    schema_one_fields = {
        "schema",
        "kind",
        "buildMode",
        "target",
        "sourceSha256",
        "sourceInputs",
        "cargoLockSha256",
        "rustcVerbose",
        "cargoVerbose",
        "effectiveCompilerInvocations",
        "effectiveCompilerInvocationsSha256",
        "environmentOverrides",
        "binarySha256",
        "binarySizeBytes",
    }
    schema_two_fields = schema_one_fields | {
        "trainingManifestSha256",
        "profileSha256",
    }
    if not isinstance(manifest, dict) or frozenset(manifest) not in {
        frozenset(schema_one_fields),
        frozenset(schema_two_fields),
    }:
        raise ValueError("build manifest fields do not match the schema")
    fields = frozenset(manifest)
    if type(manifest["schema"]) is not int or manifest["schema"] not in {1, 2}:
        raise ValueError("unsupported build manifest schema")
    if (manifest["schema"] == 1 and fields != frozenset(schema_one_fields)) or (
        manifest["schema"] == 2 and fields != frozenset(schema_two_fields)
    ):
        raise ValueError("build manifest fields do not match its schema")
    if manifest["kind"] != "nico-compositor-build":
        raise ValueError("unsupported build manifest kind")
    if not isinstance(manifest["buildMode"], str) or manifest[
        "buildMode"
    ] not in BUILD_MANIFEST_MODES:
        raise ValueError(f"unsupported build mode: {manifest['buildMode']!r}")
    pgo_build = manifest["buildMode"].endswith("-pgo")
    if pgo_build != (manifest["schema"] == 2):
        raise ValueError("PGO build mode and build manifest schema do not match")
    if pgo_build:
        _require_sha256(manifest["trainingManifestSha256"], "training manifest SHA-256")
        _require_sha256(manifest["profileSha256"], "PGO profile SHA-256")
    if not isinstance(manifest["target"], str) or manifest["target"] not in TARGETS:
        raise ValueError(f"unsupported Rust target: {manifest['target']!r}")
    for field in ("sourceSha256", "cargoLockSha256", "binarySha256"):
        _require_sha256(manifest[field], field)
    sources = manifest["sourceInputs"]
    if not isinstance(sources, list) or not sources:
        raise ValueError("source inputs must be a non-empty array")
    seen_paths = set()
    checked_sources = []
    for item in sources:
        if not isinstance(item, dict) or set(item) != {"path", "sha256"}:
            raise ValueError("source input fields are invalid")
        path = item["path"]
        if not isinstance(path, str) or not path.strip() or path in seen_paths:
            raise ValueError("source input path must be non-empty and unique")
        seen_paths.add(path)
        checked_sources.append(
            {"path": path, "sha256": _require_sha256(item["sha256"], "source input SHA-256")}
        )
    if _canonical_source_sha256(checked_sources) != manifest["sourceSha256"]:
        raise ValueError("build manifest source SHA-256 mismatch")
    lock_inputs = [
        item
        for item in checked_sources
        if item["path"] == "gpu/nico-compositord/Cargo.lock"
    ]
    if len(lock_inputs) != 1 or lock_inputs[0]["sha256"] != manifest["cargoLockSha256"]:
        raise ValueError("build manifest Cargo.lock SHA-256 mismatch")
    for field in ("rustcVerbose", "cargoVerbose"):
        if not isinstance(manifest[field], str) or not manifest[field].strip():
            raise ValueError(f"{field} must be non-empty")
    invocations = manifest["effectiveCompilerInvocations"]
    if not isinstance(invocations, list) or not invocations or not all(
        isinstance(item, str) and item.strip() for item in invocations
    ):
        raise ValueError("effective compiler invocations must be a non-empty string array")
    if pgo_build:
        use_pattern = re.compile(r"(?i)(?:^|\s)-C\s*profile-use(?:=|\s+)")
        generate_pattern = re.compile(r"(?i)(?:^|\s)-C\s*profile-generate(?:=|\s+)")
        compositor_invocations = [
            item for item in invocations if "--crate-name nico_compositord" in item
        ]
        if any(generate_pattern.search(item) for item in invocations) or not any(
            use_pattern.search(item) for item in compositor_invocations
        ):
            raise ValueError("build manifest compiler invocations do not prove profile-use")
    else:
        profile_option = re.compile(r"(?i)(?:^|\s)-C\s*profile-(?:use|generate)(?:=|\s+)")
        if any(profile_option.search(item) for item in invocations):
            raise ValueError("non-PGO build manifest contains a PGO compiler flag")
    expected_invocations_hash = hashlib.sha256(
        "\n".join(invocations).encode("utf-8")
    ).hexdigest()
    if manifest["effectiveCompilerInvocationsSha256"] != expected_invocations_hash:
        raise ValueError("effective compiler invocation SHA-256 mismatch")
    overrides = manifest["environmentOverrides"]
    if not isinstance(overrides, list):
        raise ValueError("build environment overrides must be an array")
    for item in overrides:
        if not isinstance(item, dict) or set(item) != {
            "name",
            "valueSha256",
            "action",
        }:
            raise ValueError("build environment override fields are invalid")
        if not isinstance(item["name"], str) or not item["name"].strip():
            raise ValueError("build environment override name must be non-empty")
        if not isinstance(item["action"], str) or item["action"] not in {
            "scrubbed",
            "preserved",
        }:
            raise ValueError("build environment override action is invalid")
        expected_action = (
            "preserved"
            if item["name"].upper() in BUILD_ENV_PRESERVED
            else "scrubbed"
        )
        if item["action"] != expected_action:
            raise ValueError("incorrect build environment override action")
        _require_sha256(item["valueSha256"], "environment value SHA-256")
    if type(manifest["binarySizeBytes"]) is not int or manifest["binarySizeBytes"] <= 0:
        raise ValueError("binarySizeBytes must be a positive integer")
    if not binary or manifest["binarySizeBytes"] != len(binary) or manifest[
        "binarySha256"
    ] != hashlib.sha256(binary).hexdigest():
        raise ValueError("build manifest binary SHA-256 mismatch")


def write_build_manifest(
    binary_path: Path,
    target: str,
    build_mode: str,
    source_root: Path,
    source_inputs_path: Path,
    build_log_path: Path,
    overrides_path: Path,
    output_path: Path,
    training_manifest_sha256: str | None = None,
    profile_sha256: str | None = None,
) -> None:
    source_inputs_before = json.loads(source_inputs_path.read_text(encoding="utf-8"))
    if not isinstance(source_inputs_before, list):
        raise ValueError("source input snapshot must be a JSON array")
    source_inputs = collect_build_source_inputs(source_root)
    if source_inputs != source_inputs_before:
        raise ValueError("build source inputs changed while the compositor was compiling")
    cargo_lock = source_root.resolve() / "gpu" / "nico-compositord" / "Cargo.lock"
    overrides_payload = json.loads(overrides_path.read_text(encoding="utf-8"))
    if not isinstance(overrides_payload, list):
        raise ValueError("environment override record must be a JSON array")
    binary = binary_path.read_bytes()
    manifest = create_build_manifest(
        binary,
        build_mode=build_mode,
        target=target,
        source_inputs=source_inputs,
        cargo_lock_sha256=hashlib.sha256(cargo_lock.read_bytes()).hexdigest(),
        rustc_verbose=subprocess.check_output(["rustc", "-vV"], text=True),
        cargo_verbose=subprocess.check_output(["cargo", "-Vv"], text=True),
        effective_compiler_invocations=parse_cargo_rustc_invocations(
            build_log_path.read_text(encoding="utf-8", errors="replace")
        ),
        environment_overrides=overrides_payload,
        training_manifest_sha256=training_manifest_sha256,
        profile_sha256=profile_sha256,
    )
    validate_build_manifest(manifest, binary)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    temporary = output_path.with_name(output_path.name + ".tmp")
    temporary.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    temporary.replace(output_path)


def write_manifest(
    binary_path: Path, target: str, output_path: Path, *, runner=run_helper_bounded
) -> None:
    binary_path = binary_path.resolve()
    binary = binary_path.read_bytes()
    manifest = create_manifest(binary, target)
    goos, _ = TARGETS[target]
    inspection = inspect_helper_protocol(binary_path, runner, goos=goos)
    if (
        hashlib.sha256(binary_path.read_bytes()).digest()
        != hashlib.sha256(binary).digest()
    ):
        raise ValueError("helper binary changed while being inspected")
    if inspection["protocol"] == "NCT2":
        manifest.update(
            {
                "protocol": "NCT2",
                "protocolVersion": inspection["protocolVersion"],
                "inputMode": inspection["inputMode"],
                "outputFormat": inspection["outputFormat"],
                "capabilities": inspection["capabilities"],
            }
        )
    output_path.parent.mkdir(parents=True, exist_ok=True)
    temporary = output_path.with_name(output_path.name + ".tmp")
    temporary.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    temporary.replace(output_path)


def write_dependency_bundle(
    cargo_manifest: Path, target: str, output_directory: Path
) -> None:
    command = [
        "cargo",
        "metadata",
        "--locked",
        "--format-version",
        "1",
        "--manifest-path",
        str(cargo_manifest),
        "--filter-platform",
        target,
    ]
    metadata = json.loads(subprocess.check_output(command, text=True))
    resolve = metadata.get("resolve") or {}
    nodes = {node["id"]: node for node in resolve.get("nodes", [])}
    root_id = resolve.get("root")
    if not root_id or root_id not in nodes:
        raise ValueError("cargo metadata did not report a resolved workspace root")
    reachable = set()
    pending = [root_id]
    while pending:
        package_id = pending.pop()
        if package_id in reachable:
            continue
        reachable.add(package_id)
        node = nodes.get(package_id)
        if node is None:
            continue
        for dependency in node.get("deps", []):
            kinds = dependency.get("dep_kinds", [])
            if not kinds or any(kind.get("kind") != "dev" for kind in kinds):
                pending.append(dependency["pkg"])
    selected_ids = reachable - {root_id}
    crate_root = cargo_manifest.resolve().parent
    repository_license = Path(__file__).resolve().parents[1] / "LICENSE"
    license_root = output_directory / "third-party-licenses"
    records = []
    packages = [
        package
        for package in metadata["packages"]
        if package["id"] in selected_ids | {root_id}
    ]
    for package in sorted(packages, key=lambda item: (item["name"], item["version"])):
        package_root = Path(package["manifest_path"]).resolve().parent
        if package.get("source") is None:
            package_root = crate_root
        candidates = []
        declared_file = package.get("license_file")
        if declared_file:
            declared = Path(declared_file)
            candidates.append(
                declared if declared.is_absolute() else package_root / declared
            )
        candidates.extend(
            path
            for path in package_root.iterdir()
            if path.is_file()
            and path.name.upper().startswith(
                ("LICENSE", "COPYING", "NOTICE", "PATENTS")
            )
        )
        unique = {path.resolve() for path in candidates if path.is_file()}
        if (
            package.get("source") is None
            and not unique
            and repository_license.is_file()
        ):
            unique.add(repository_license.resolve())
        if not package.get("license") and not declared_file:
            raise ValueError(
                f"package has no license metadata: {package['name']} {package['version']}"
            )
        if declared_file and not unique:
            raise ValueError(
                f"declared license file is missing: {package['name']} {package['version']} {declared_file}"
            )
        destination = license_root / f"{package['name']}-{package['version']}"
        destination.mkdir(parents=True, exist_ok=True)
        copied = []
        for source in sorted(unique):
            target_path = destination / source.name
            shutil.copyfile(source, target_path)
            copied.append(target_path.relative_to(output_directory).as_posix())
        records.append(
            {
                "name": package["name"],
                "version": package["version"],
                "license": package.get("license") or "see license file",
                "files": copied,
            }
        )

    output_directory.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(crate_root / "Cargo.lock", output_directory / "Cargo.lock")
    shutil.copyfile(cargo_manifest, output_directory / "Cargo.toml")
    lines = [
        "# Third-party notices",
        "",
        f"This NCT1 compositor is built with wgpu {WGPU_VERSION}. The locked package inventory and license texts are included below.",
        "",
    ]
    for record in records:
        files = ", ".join(f"`{path}`" for path in record["files"])
        if not files:
            files = "no license text was present in the Cargo package; SPDX identifier recorded"
        lines.append(
            f"- `{record['name']} {record['version']}` — {record['license']}; {files}"
        )
    temporary = output_directory / "THIRD_PARTY_NOTICES.md.tmp"
    temporary.write_text("\n".join(lines) + "\n", encoding="utf-8")
    temporary.replace(output_directory / "THIRD_PARTY_NOTICES.md")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cargo-manifest", type=Path)
    parser.add_argument("--notices-output-directory", type=Path)
    args = parser.parse_args()
    if bool(args.cargo_manifest) != bool(args.notices_output_directory):
        parser.error(
            "--cargo-manifest and --notices-output-directory must be supplied together"
        )
    if args.cargo_manifest:
        write_dependency_bundle(
            args.cargo_manifest, args.target, args.notices_output_directory
        )
    write_manifest(args.binary, args.target, args.output)
    print(args.output)


def build_provenance_main(argv: list[str]) -> None:
    parser = argparse.ArgumentParser(description="Write experiment-only build provenance.")
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--mode", required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--source-inputs", type=Path, required=True)
    parser.add_argument("--build-log", type=Path, required=True)
    parser.add_argument("--overrides-json", type=Path, required=True)
    parser.add_argument("--training-manifest-sha256")
    parser.add_argument("--profile-sha256")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    write_build_manifest(
        args.binary,
        args.target,
        args.mode,
        args.source_root,
        args.source_inputs,
        args.build_log,
        args.overrides_json,
        args.output,
        args.training_manifest_sha256,
        args.profile_sha256,
    )
    print(args.output)


def source_inputs_main(argv: list[str]) -> None:
    parser = argparse.ArgumentParser(description="Snapshot source inputs for a compositor build.")
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    records = collect_build_source_inputs(args.source_root)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    temporary = args.output.with_name(args.output.name + ".tmp")
    temporary.write_text(json.dumps(records, indent=2) + "\n", encoding="utf-8")
    temporary.replace(args.output)
    print(args.output)


if __name__ == "__main__":
    if sys.argv[1:2] == ["build-provenance"]:
        build_provenance_main(sys.argv[2:])
    elif sys.argv[1:2] == ["source-inputs"]:
        source_inputs_main(sys.argv[2:])
    else:
        main()
