"""T9.3 contracts for the profile-use build and its PGO provenance link."""

import importlib.util
import shutil
import subprocess
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[3]
MANIFEST_SCRIPT = ROOT / "scripts" / "nico_timeline_manifest.py"
SPEC = importlib.util.spec_from_file_location("nico_timeline_manifest_pgo", MANIFEST_SCRIPT)
MANIFEST = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MANIFEST)


def build_kwargs(mode="release-pgo", training_hash="a" * 64):
    return {
        "build_mode": mode,
        "target": "x86_64-pc-windows-msvc",
        "source_inputs": [
            {"path": "gpu/nico-compositord/Cargo.lock", "sha256": "2" * 64},
            {"path": "gpu/nico-compositord/src/lib.rs", "sha256": "3" * 64},
        ],
        "cargo_lock_sha256": "2" * 64,
        "rustc_verbose": "rustc 1.96.0\nLLVM version: 22.1.2\n",
        "cargo_verbose": "cargo 1.96.0\ncommit-hash: example\n",
        "effective_compiler_invocations": [
            "Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto -C profile-use=C:/owned/profile.profdata`"
        ],
        "environment_overrides": [],
        "training_manifest_sha256": training_hash,
        "profile_sha256": "b" * 64 if training_hash is not None else None,
    }


def test_profile_use_build_records_the_training_manifest_and_separate_schema():
    binary = b"profile-use helper bytes"
    manifest = MANIFEST.create_build_manifest(binary, **build_kwargs())

    assert manifest["schema"] == 2
    assert manifest["buildMode"] == "release-pgo"
    assert manifest["trainingManifestSha256"] == "a" * 64
    MANIFEST.validate_build_manifest(manifest, binary)


@pytest.mark.parametrize("training_hash", [None, "", "0" * 63, "g" * 64])
def test_profile_use_build_requires_a_valid_training_manifest_sha256(training_hash):
    with pytest.raises(ValueError, match="training manifest"):
        MANIFEST.create_build_manifest(
            b"helper", **build_kwargs(training_hash=training_hash)
        )


def test_profile_use_build_rejects_a_generate_profile_flag():
    kwargs = build_kwargs()
    kwargs["effective_compiler_invocations"] = [
        "Running `rustc --crate-name nico_compositord -C profile-generate=C:/tmp/raw`"
    ]
    with pytest.raises(ValueError, match="profile-use"):
        MANIFEST.create_build_manifest(b"helper", **kwargs)


def test_profile_use_build_must_not_claim_training_identity_for_a_non_pgo_mode():
    with pytest.raises(ValueError, match="training manifest"):
        MANIFEST.create_build_manifest(
            b"helper", **build_kwargs(mode="release", training_hash="a" * 64)
        )


def test_regular_build_manifest_remains_schema_one_without_pgo_identity():
    kwargs = build_kwargs(mode="release", training_hash=None)
    kwargs["effective_compiler_invocations"] = [
        "Running `rustc --crate-name nico_compositord -C opt-level=3 -C lto`"
    ]
    manifest = MANIFEST.create_build_manifest(b"regular helper", **kwargs)

    assert manifest["schema"] == 1
    assert "trainingManifestSha256" not in manifest
    MANIFEST.validate_build_manifest(manifest, b"regular helper")


def test_profile_use_selector_is_opt_in_and_requires_a_pgo_run_root():
    source = (ROOT / "scripts/experiments/nico-timeline/build-variants.ps1").read_text(
        encoding="utf-8"
    )

    assert '"$_-pgo"' in source
    assert "PgoRunRoot" in source
    assert "profile-use" in source


def test_pgo_entry_point_can_dispatch_the_selected_profile_use_build():
    source = (ROOT / "scripts/experiments/nico-timeline/pgo.ps1").read_text(
        encoding="utf-8"
    )

    assert "BuildProfileUse" in source
    assert "build-variants.ps1" in source


def test_pgo_profile_use_dispatch_requires_both_owned_roots(tmp_path):
    pwsh = shutil.which("pwsh")
    if not pwsh:
        pytest.fail("pwsh is required to exercise the PGO build selector")
    script = ROOT / "scripts/experiments/nico-timeline/pgo.ps1"
    result = subprocess.run(
        [
            pwsh,
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-File",
            str(script),
            "-BuildProfileUse",
            "-RunRoot",
            str(tmp_path / "build-output"),
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert result.returncode != 0
    assert "PgoRunRoot is required" in (result.stdout + result.stderr)
