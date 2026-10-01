import hashlib
import importlib.util
import json
import os
import shutil
import subprocess
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[3]
MANIFEST_SCRIPT = ROOT / "scripts" / "nico_timeline_manifest.py"
RUNNER_SCRIPT = ROOT / "scripts" / "experiments" / "nico-timeline" / "build-variants.ps1"
SPEC = importlib.util.spec_from_file_location("nico_timeline_manifest", MANIFEST_SCRIPT)
MANIFEST = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MANIFEST)


def build_manifest_kwargs():
    source_inputs = [
        {"path": "gpu/nico-compositord/Cargo.lock", "sha256": "2" * 64},
        {"path": "gpu/nico-compositord/src/lib.rs", "sha256": "3" * 64},
    ]
    return {
        "build_mode": "release",
        "target": "x86_64-pc-windows-msvc",
        "source_inputs": source_inputs,
        "cargo_lock_sha256": "2" * 64,
        "rustc_verbose": "rustc 1.96.0\nLLVM version: 22.1.2\n",
        "cargo_verbose": "cargo 1.96.0\ncommit-hash: example\n",
        "effective_compiler_invocations": [
            "Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto`"
        ],
        "environment_overrides": [
            {
                "name": "RUSTFLAGS",
                "value_sha256": hashlib.sha256(b"-C target-cpu=native").hexdigest(),
                "action": "scrubbed",
            }
        ],
    }


def test_build_manifest_records_provenance_without_changing_runtime_manifest():
    binary = b"built helper bytes"
    runtime = MANIFEST.create_manifest(binary, "x86_64-pc-windows-msvc")
    build = MANIFEST.create_build_manifest(binary, **build_manifest_kwargs())

    assert runtime == {
        "schema": 1,
        "protocol": "NCT1",
        "os": "windows",
        "architecture": "amd64",
        "sha256": hashlib.sha256(binary).hexdigest(),
        "wgpuVersion": "0.20.1",
        "cargoTarget": "x86_64-pc-windows-msvc",
    }
    assert build["kind"] == "nico-compositor-build"
    assert build["buildMode"] == "release"
    assert build["sourceSha256"] == MANIFEST._canonical_source_sha256(
        build_manifest_kwargs()["source_inputs"]
    )
    assert build["cargoLockSha256"] == "2" * 64
    assert build["effectiveCompilerInvocations"] == build_manifest_kwargs()[
        "effective_compiler_invocations"
    ]
    assert build["binarySha256"] == hashlib.sha256(binary).hexdigest()
    assert build["environmentOverrides"][0]["name"] == "RUSTFLAGS"
    assert build["environmentOverrides"][0]["action"] == "scrubbed"
    encoded = json.dumps(build)
    assert "-C target-cpu=native" not in encoded
    assert '"value":' not in encoded
    MANIFEST.validate_build_manifest(build, binary)


@pytest.mark.parametrize("build_mode", [None, "", "release-invalid", "debug"])
def test_build_manifest_rejects_missing_or_unsupported_mode(build_mode):
    kwargs = build_manifest_kwargs()
    kwargs["build_mode"] = build_mode
    with pytest.raises(ValueError, match="build mode"):
        MANIFEST.create_build_manifest(b"built helper bytes", **kwargs)


def test_build_manifest_rejects_binary_hash_mismatch():
    manifest = MANIFEST.create_build_manifest(b"original", **build_manifest_kwargs())
    with pytest.raises(ValueError, match="binary SHA-256 mismatch"):
        MANIFEST.validate_build_manifest(manifest, b"tampered")


def test_build_manifest_rejects_tampered_recorded_hash():
    manifest = MANIFEST.create_build_manifest(b"original", **build_manifest_kwargs())
    manifest["binarySha256"] = "0" * 64
    with pytest.raises(ValueError, match="binary SHA-256 mismatch"):
        MANIFEST.validate_build_manifest(manifest, b"original")


def test_hidden_build_overrides_are_detected_without_recording_values():
    overrides = MANIFEST.collect_build_environment_overrides(
        {
            "RUSTFLAGS": "-C target-cpu=native",
            "CARGO_ENCODED_RUSTFLAGS": "-Clto=off",
            "CARGO_PROFILE_RELEASE_LTO": "false",
            "CARGO_PROFILE_RELEASE_CODEGEN_UNITS": "1",
            "CARGO_TARGET_X86_64_PC_WINDOWS_MSVC_RUSTFLAGS": "-C opt-level=1",
            "RUSTC_WRAPPER": "wrapper.exe --secret-looking-argument",
            "UNRELATED_SETTING": "leave me alone",
        }
    )

    assert [item["name"] for item in overrides] == [
        "CARGO_ENCODED_RUSTFLAGS",
        "CARGO_PROFILE_RELEASE_CODEGEN_UNITS",
        "CARGO_PROFILE_RELEASE_LTO",
        "CARGO_TARGET_X86_64_PC_WINDOWS_MSVC_RUSTFLAGS",
        "RUSTC_WRAPPER",
        "RUSTFLAGS",
    ]
    assert all("value" not in item for item in overrides)
    assert overrides[-1]["value_sha256"] == hashlib.sha256(
        b"-C target-cpu=native"
    ).hexdigest()
    assert all(item["action"] == "scrubbed" for item in overrides)


def test_effective_rustc_invocations_are_extracted_from_verbose_cargo_log():
    log = """\
warning: ignored warning
     Running `rustc --crate-name dependency -C opt-level=3 --target x86_64-pc-windows-msvc`
     Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto`
"""
    assert MANIFEST.parse_cargo_rustc_invocations(log) == [
        "Running `rustc --crate-name dependency -C opt-level=3 --target x86_64-pc-windows-msvc`",
        "Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto`",
    ]
    with pytest.raises(ValueError, match="no rustc compiler invocations"):
        MANIFEST.parse_cargo_rustc_invocations("Finished release [optimized] target(s)")


def test_windows_cargo_environment_wrapper_yields_only_the_rustc_command():
    log = r"""     Running `set CARGO=C:\toolchain\bin\cargo.exe&& set CARGO_CRATE_NAME=dependency&& set PATH="C:\temp\deps;C:\toolchain\bin"&& C:\toolchain\bin\rustc.exe --crate-name dependency --edition=2021 -C opt-level=3 --target x86_64-pc-windows-msvc`"""

    assert MANIFEST.parse_cargo_rustc_invocations(log) == [
        r"Running `C:\toolchain\bin\rustc.exe --crate-name dependency --edition=2021 -C opt-level=3 --target x86_64-pc-windows-msvc`"
    ]


def test_build_provenance_cli_writes_a_separate_hash_bound_sidecar(tmp_path):
    binary = tmp_path / "helper.exe"
    binary.write_bytes(b"owned test helper")
    build_log = tmp_path / "cargo.log"
    build_log.write_text(
        "Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto`\n",
        encoding="utf-8",
    )
    overrides_path = tmp_path / "overrides.json"
    overrides_path.write_text("[]\n", encoding="utf-8")
    source_inputs_path = tmp_path / "source-inputs.json"
    source_inputs_path.write_text(
        json.dumps(MANIFEST.collect_build_source_inputs(ROOT)) + "\n",
        encoding="utf-8",
    )
    output = tmp_path / "build-manifest.json"

    MANIFEST.write_build_manifest(
        binary,
        "x86_64-pc-windows-msvc",
        "release",
        ROOT,
        source_inputs_path,
        build_log,
        overrides_path,
        output,
    )

    manifest = json.loads(output.read_text(encoding="utf-8"))
    MANIFEST.validate_build_manifest(manifest, binary.read_bytes())
    assert manifest["binarySha256"] == hashlib.sha256(binary.read_bytes()).hexdigest()
    assert manifest["sourceInputs"]
    assert manifest["effectiveCompilerInvocations"][0].startswith("Running `rustc")
    assert "protocol" not in manifest
    assert not Path(str(output) + ".tmp").exists()


def test_build_variants_requires_an_explicit_mode_and_child_runner_scrubs_overrides(
    tmp_path,
):
    pwsh = shutil.which("pwsh")
    if not pwsh:
        pytest.fail("pwsh is required to exercise the build-variant runner")

    missing_mode = subprocess.run(
        [pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(RUNNER_SCRIPT)],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert missing_mode.returncode != 0
    assert "Mode" in (missing_mode.stdout + missing_mode.stderr)

    environment = dict(os.environ)
    environment["RUSTFLAGS"] = "-C target-cpu=native"
    environment["CARGO_PROFILE_RELEASE_LTO"] = "false"
    preflight = subprocess.run(
        [
            pwsh,
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-File",
            str(RUNNER_SCRIPT),
            "-Mode",
            "release",
            "-RunRoot",
            str(tmp_path / "preflight"),
            "-ValidateOnly",
        ],
        capture_output=True,
        text=True,
        env=environment,
        timeout=20,
        check=False,
    )
    assert preflight.returncode == 0, preflight.stdout + preflight.stderr
    report = json.loads(preflight.stdout)
    assert {item["name"] for item in report["environmentOverrides"]} >= {
        "RUSTFLAGS",
        "CARGO_PROFILE_RELEASE_LTO",
    }
    assert {
        item["name"]: item["action"] for item in report["environmentOverrides"]
    }["RUSTFLAGS"] == "scrubbed"
    assert set(report["scrubbedEnvironmentNames"]) >= {
        "RUSTFLAGS",
        "CARGO_PROFILE_RELEASE_LTO",
    }
    assert "target-cpu=native" not in preflight.stdout


def test_build_variants_writes_empty_override_array_and_provenance(tmp_path):
    pwsh = shutil.which("pwsh")
    if not pwsh:
        pytest.fail("pwsh is required to exercise the build-variant runner")

    evidence_path = (
        ROOT
        / "docs"
        / "verification"
        / "niconico-comments"
        / "t8-1-build-provenance-2026-09-28.json"
    )
    baseline = json.loads(evidence_path.read_text(encoding="utf-8"))["fresh_build"][
        "binary"
    ]
    baseline_binary = Path(baseline["path"])
    assert baseline_binary.is_file()
    assert hashlib.sha256(baseline_binary.read_bytes()).hexdigest() == baseline[
        "sha256"
    ]

    run_root = tmp_path / "zero-overrides-run"
    harness = tmp_path / "run-zero-overrides.ps1"

    def ps_quote(value):
        return "'" + str(value).replace("'", "''") + "'"

    harness.write_text(
        f"""$ErrorActionPreference = 'Stop'
$global:ActualCargoExe = (Get-Command cargo.exe -ErrorAction Stop).Source
$global:FakeNicoHelper = {ps_quote(baseline_binary)}
Get-ChildItem Env: | Where-Object {{
    $name = $_.Name.ToUpperInvariant()
    $name -in @('CARGO_BUILD_TARGET', 'CARGO_ENCODED_RUSTFLAGS', 'CARGO_HOME', 'CARGO_TARGET_DIR', 'RUSTC', 'RUSTC_WORKSPACE_WRAPPER', 'RUSTC_WRAPPER', 'RUSTFLAGS', 'RUSTUP_HOME', 'RUSTUP_TOOLCHAIN') -or
    $name.StartsWith('CARGO_PROFILE_') -or
    ($name.StartsWith('CARGO_TARGET_') -and $name.EndsWith('_RUSTFLAGS'))
}} | ForEach-Object {{ Remove-Item -LiteralPath ('Env:' + $_.Name) }}
function global:cargo {{
    $cargoArgs = @($args)
    if ($cargoArgs.Count -gt 0 -and $cargoArgs[0] -eq '-Vv') {{
        & $global:ActualCargoExe @cargoArgs
        return
    }}
    if ($cargoArgs.Count -eq 0 -or $cargoArgs[0] -ne 'build') {{
        throw 'Unexpected Cargo shim arguments.'
    }}
    $profileIndex = [Array]::IndexOf($cargoArgs, '--profile')
    if ($profileIndex -lt 0) {{ throw 'Runner omitted --profile.' }}
    $profile = $cargoArgs[$profileIndex + 1]
    $binaryDirectory = Join-Path $env:CARGO_TARGET_DIR (Join-Path $env:CARGO_BUILD_TARGET (Join-Path $profile ''))
    [void](New-Item -ItemType Directory -Force -Path $binaryDirectory)
    Copy-Item -LiteralPath $global:FakeNicoHelper -Destination (Join-Path $binaryDirectory 'nico-compositord.exe')
    Write-Output 'Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto=thin --target x86_64-pc-windows-msvc`'
    $global:LASTEXITCODE = 0
}}
& {ps_quote(RUNNER_SCRIPT)} -Mode release-thin -RunRoot {ps_quote(run_root)}
""",
        encoding="utf-8",
    )

    environment = dict(os.environ)
    for name in list(environment):
        upper_name = name.upper()
        if (
            upper_name
            in {
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
            or upper_name.startswith("CARGO_PROFILE_")
            or (
                upper_name.startswith("CARGO_TARGET_")
                and upper_name.endswith("_RUSTFLAGS")
            )
        ):
            environment.pop(name)

    result = subprocess.run(
        [pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(harness)],
        capture_output=True,
        text=True,
        env=environment,
        timeout=90,
        check=False,
    )
    assert result.returncode == 0, result.stdout + result.stderr

    artifact_dir = run_root / "artifacts" / "x86_64-pc-windows-msvc" / "release-thin"
    runtime_path = artifact_dir / "runtime-manifest.json"
    build_path = artifact_dir / "build-manifest.json"
    runtime = json.loads(runtime_path.read_text(encoding="utf-8"))
    build = json.loads(build_path.read_text(encoding="utf-8"))
    MANIFEST.validate_build_manifest(build, baseline_binary.read_bytes())
    assert build["buildMode"] == "release-thin"
    assert build["environmentOverrides"] == []
    assert any(
        "-C lto=thin" in invocation
        for invocation in build["effectiveCompilerInvocations"]
    )
    assert set(runtime) == {
        "schema",
        "protocol",
        "os",
        "architecture",
        "sha256",
        "wgpuVersion",
        "cargoTarget",
        "protocolVersion",
        "inputMode",
        "outputFormat",
        "capabilities",
    }
