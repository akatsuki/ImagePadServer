"""T9.1 contract tests for the PGO training/evaluation manifest validator.

The validator is exposed by pgo.ps1 in manifest-validation mode. Manifests use
schemaVersion=1, a common buildIdentity object, a role-specific materials list,
and profileMetadata on the evaluation manifest.
"""

import copy
import hashlib
import json
import shutil
import subprocess
from pathlib import Path

import pytest


SCRIPT = Path(__file__).with_name("pgo.ps1")
POWERSHELL = shutil.which("pwsh")
REQUIRED_COVERAGE = {"short", "long", "dense", "NCT1", "NCT2"}


def sha(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def material(label, coverage, *, seed=1):
    return {
        "id": label,
        "coverage": coverage,
        "sourceSha256": sha("source:" + label),
        "snapshotSha256": sha("snapshot:" + label),
        "seed": seed,
    }


def manifests():
    identity = {
        "buildSourceSnapshotSha256": sha("build-source-v1"),
        "rustcVersion": "rustc 1.96.0 (stable)",
        "cargoVersion": "cargo 1.96.0 (stable)",
        "llvmVersion": "22.1.2",
        "target": "x86_64-pc-windows-msvc",
    }
    training = {
        "schemaVersion": 1,
        "role": "training",
        "buildIdentity": copy.deepcopy(identity),
        "materials": [
            material("train-short", "short"),
            material("train-long", "long"),
            material("train-dense", "dense"),
            material("train-nct1", "NCT1"),
            material("train-nct2", "NCT2"),
        ],
    }
    evaluation = {
        "schemaVersion": 1,
        "role": "evaluation",
        "buildIdentity": copy.deepcopy(identity),
        "materials": [
            material("eval-short", "short"),
            material("eval-long", "long"),
            material("eval-dense", "dense"),
            material("eval-nct1", "NCT1"),
            material("eval-nct2", "NCT2"),
        ],
        "profileMetadata": {
            "trainingManifestSha256": "pending",
            "sourceSnapshotSha256": identity["buildSourceSnapshotSha256"],
            "rustcVersion": identity["rustcVersion"],
            "cargoVersion": identity["cargoVersion"],
            "llvmVersion": identity["llvmVersion"],
            "target": identity["target"],
            "profileSha256": sha("merged-profile"),
            "instrumented": False,
        },
    }
    return training, evaluation


def validate(tmp_path, training, evaluation, *, recompute_training_hash=True):
    assert SCRIPT.is_file(), (
        "T9.1 PGO manifest validator entry point is not implemented: "
        "scripts/experiments/nico-timeline/pgo.ps1"
    )
    if recompute_training_hash:
        evaluation["profileMetadata"]["trainingManifestSha256"] = hashlib.sha256(
            json.dumps(training, sort_keys=True, separators=(",", ":")).encode("utf-8")
        ).hexdigest()
    train_path = tmp_path / "training.json"
    eval_path = tmp_path / "evaluation.json"
    train_path.write_text(json.dumps(training), encoding="utf-8")
    eval_path.write_text(json.dumps(evaluation), encoding="utf-8")
    return subprocess.run(
        [
            POWERSHELL,
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-File",
            str(SCRIPT),
            "-ValidateManifests",
            "-TrainingManifest",
            str(train_path),
            "-EvaluationManifest",
            str(eval_path),
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


@pytest.fixture(autouse=True)
def require_validator_runtime():
    if POWERSHELL is None:
        pytest.fail("pwsh is required to exercise the PGO manifest validator")


def test_valid_role_manifests_with_all_coverage_are_accepted(tmp_path):
    training, evaluation = manifests()
    result = validate(tmp_path, training, evaluation)
    assert result.returncode == 0, result.stdout + result.stderr
    assert "manifests_valid=true" in result.stdout


@pytest.mark.parametrize("role", ["training", "evaluation"])
@pytest.mark.parametrize("coverage", sorted(REQUIRED_COVERAGE))
def test_each_manifest_role_must_cover_every_planned_material_class(
    tmp_path, role, coverage
):
    training, evaluation = manifests()
    chosen = training if role == "training" else evaluation
    chosen["materials"] = [
        item for item in chosen["materials"] if item["coverage"] != coverage
    ]
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert coverage in result.stdout + result.stderr
    assert "manifests_valid=true" not in result.stdout


def test_source_snapshot_pair_overlap_is_rejected_even_when_seed_differs(tmp_path):
    training, evaluation = manifests()
    training["materials"][0]["sourceSha256"] = "a" * 64
    training["materials"][0]["snapshotSha256"] = "b" * 64
    training["materials"][0]["seed"] = 3
    evaluation["materials"][0]["sourceSha256"] = "a" * 64
    evaluation["materials"][0]["snapshotSha256"] = "b" * 64
    evaluation["materials"][0]["seed"] = 999
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert "overlap" in (result.stdout + result.stderr).lower()


@pytest.mark.parametrize(
    ("identity_key", "bad_value"),
    [
        ("buildSourceSnapshotSha256", "c" * 64),
        ("rustcVersion", "rustc 1.95.0"),
        ("cargoVersion", "cargo 1.95.0"),
        ("llvmVersion", "21.1.0"),
        ("target", "aarch64-pc-windows-msvc"),
    ],
)
def test_training_and_evaluation_build_identity_must_match(
    tmp_path, identity_key, bad_value
):
    training, evaluation = manifests()
    evaluation["buildIdentity"][identity_key] = bad_value
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert identity_key in result.stdout + result.stderr


@pytest.mark.parametrize(
    "field",
    [
        "sourceSnapshotSha256",
        "rustcVersion",
        "cargoVersion",
        "llvmVersion",
        "target",
        "trainingManifestSha256",
    ],
)
def test_stale_profile_metadata_must_match_current_build_identity(tmp_path, field):
    training, evaluation = manifests()
    value = {
        "sourceSnapshotSha256": "buildSourceSnapshotSha256",
        "rustcVersion": "rustcVersion",
        "cargoVersion": "cargoVersion",
        "llvmVersion": "llvmVersion",
        "target": "target",
    }
    if field == "trainingManifestSha256":
        evaluation["profileMetadata"][field] = "0" * 64
    else:
        identity_field = value[field]
        evaluation["profileMetadata"][field] = "stale-" + str(
            evaluation["buildIdentity"][identity_field]
        )
    result = validate(
        tmp_path,
        training,
        evaluation,
        recompute_training_hash=field != "trainingManifestSha256",
    )
    assert result.returncode != 0
    assert field in result.stdout + result.stderr


@pytest.mark.parametrize("role", ["training", "evaluation"])
def test_manifest_role_is_strictly_enforced(tmp_path, role):
    training, evaluation = manifests()
    chosen = training if role == "training" else evaluation
    chosen["role"] = "evaluation" if role == "training" else "training"
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert role in result.stdout + result.stderr


@pytest.mark.parametrize("role", ["training", "evaluation"])
def test_manifest_required_top_level_fields_are_strict(tmp_path, role):
    training, evaluation = manifests()
    chosen = training if role == "training" else evaluation
    del chosen["buildIdentity"]
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert "buildIdentity" in result.stdout + result.stderr


@pytest.mark.parametrize("role", ["training", "evaluation"])
@pytest.mark.parametrize("schema_version", [0, 2, "1"])
def test_manifest_schema_version_must_be_integer_one(tmp_path, role, schema_version):
    training, evaluation = manifests()
    chosen = training if role == "training" else evaluation
    chosen["schemaVersion"] = schema_version
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert "schemaVersion" in result.stdout + result.stderr


def test_material_hash_must_be_a_sha256_digest(tmp_path):
    training, evaluation = manifests()
    training["materials"][0]["sourceSha256"] = "not-a-sha256"
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert "sourceSha256" in result.stdout + result.stderr


def test_instrumented_profile_is_rejected_for_evaluation(tmp_path):
    training, evaluation = manifests()
    evaluation["profileMetadata"]["instrumented"] = True
    result = validate(tmp_path, training, evaluation)
    assert result.returncode != 0
    assert "instrumented" in result.stdout + result.stderr
