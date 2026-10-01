import json
import shutil
import subprocess
from pathlib import Path

import pytest


SCRIPT = Path(__file__).with_name("next-speed-benchmark.ps1")
POWERSHELL = shutil.which("pwsh")


@pytest.fixture(autouse=True)
def require_powershell():
    if POWERSHELL is None:
        pytest.fail("pwsh is required to exercise the runner manifest validator")


def manifest(**overrides):
    value = {
        "plan_version": 4,
        "task_id": "T0.5",
        "candidate_id": "candidate-a",
        "baseline_id": "nct-v3-sm9-1080p30-20260925-2dfe24f70069",
        "pair_id": "pair-0001",
        "session_mode": "cold",
        "process_generation": 1,
        "browser_reused": False,
        "stream_protocol": "NCT1",
        "build_profile": "release",
        "training_manifest_hash": None,
        "run_status": "completed",
        "skip_reason": None,
    }
    value.update(overrides)
    return value


def validate(tmp_path, value, *, requested_mode="cold", requested_protocol="NCT1"):
    path = tmp_path / "run-manifest.json"
    path.write_text(json.dumps(value), encoding="utf-8")
    return subprocess.run(
        [
            POWERSHELL,
            "-NoProfile",
            "-File",
            str(SCRIPT),
            "-RunManifest",
            str(path),
            "-RequestedSessionMode",
            requested_mode,
            "-RequestedStreamProtocol",
            requested_protocol,
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


def test_valid_manifest_is_an_eligible_sample(tmp_path):
    result = validate(tmp_path, manifest())
    assert result.returncode == 0, result.stderr
    assert "sample_eligible=true" in result.stdout


def test_previous_plan_version_three_is_rejected(tmp_path):
    result = validate(tmp_path, manifest(plan_version=3))
    assert result.returncode != 0
    assert "plan_version must be integer 4" in result.stderr


def test_warm_nct2_manifest_is_eligible_when_both_modes_match(tmp_path):
    result = validate(
        tmp_path,
        manifest(session_mode="warm", stream_protocol="NCT2", browser_reused=True),
        requested_mode="warm",
        requested_protocol="NCT2",
    )
    assert result.returncode == 0, result.stderr
    assert "sample_eligible=true" in result.stdout


@pytest.mark.parametrize(
    "field",
    [
        "plan_version",
        "task_id",
        "candidate_id",
        "baseline_id",
        "pair_id",
        "session_mode",
        "process_generation",
        "browser_reused",
        "stream_protocol",
        "build_profile",
        "training_manifest_hash",
        "run_status",
    ],
)
def test_each_required_manifest_field_is_rejected_when_missing(tmp_path, field):
    value = manifest()
    del value[field]
    result = validate(tmp_path, value)
    assert result.returncode != 0
    assert field in result.stderr
    assert "sample_eligible=true" not in result.stdout


def test_missing_session_mode_is_not_accepted_as_a_sample(tmp_path):
    value = manifest()
    del value["session_mode"]
    result = validate(tmp_path, value)
    assert result.returncode != 0
    assert "session_mode" in result.stderr


def test_warm_request_rejects_cold_actual_manifest(tmp_path):
    result = validate(tmp_path, manifest(session_mode="cold"), requested_mode="warm")
    assert result.returncode != 0
    assert "session_mode" in result.stderr


def test_nct2_request_rejects_nct1_actual_manifest(tmp_path):
    result = validate(tmp_path, manifest(stream_protocol="NCT1"), requested_protocol="NCT2")
    assert result.returncode != 0
    assert "stream_protocol" in result.stderr


def test_skip_is_validly_recorded_but_not_a_success_sample(tmp_path):
    result = validate(
        tmp_path,
        manifest(
            run_status="skipped",
            session_mode="warm",
            skip_reason="T0.4 only implements cold sessions.",
        ),
        requested_mode="warm",
    )
    assert result.returncode == 0, result.stderr
    assert "run_status=skipped" in result.stdout
    assert "sample_eligible=false" in result.stdout


def test_skipped_run_requires_a_reason_not_testable_field(tmp_path):
    value = manifest(run_status="skipped", session_mode="warm")
    del value["skip_reason"]

    result = validate(tmp_path, value, requested_mode="warm")
    assert result.returncode != 0
    assert "skip_reason" in result.stderr
    assert "sample_eligible=true" not in result.stdout


@pytest.mark.parametrize("target_field", ["session_mode", "stream_protocol"])
def test_skipped_run_requires_the_target_fields_used_to_explain_its_reason(
    tmp_path, target_field
):
    value = manifest(
        run_status="skipped",
        session_mode="warm",
        skip_reason="T0.4 only implements cold sessions.",
    )
    del value[target_field]

    result = validate(tmp_path, value, requested_mode="warm")
    assert result.returncode != 0
    assert target_field in result.stderr
    assert "sample_eligible=true" not in result.stdout


@pytest.mark.parametrize(
    ("session_mode", "stream_protocol", "reason"),
    [
        ("warm", "NCT1", "T0.4 only implements cold sessions."),
        ("cold", "NCT2", "T0.4 only implements NCT1."),
        # The producer checks session mode before stream protocol.
        ("warm", "NCT2", "T0.4 only implements cold sessions."),
    ],
)
def test_skipped_run_accepts_reason_for_actual_unsupported_target(
    tmp_path, session_mode, stream_protocol, reason
):
    result = validate(
        tmp_path,
        manifest(
            run_status="skipped",
            session_mode=session_mode,
            stream_protocol=stream_protocol,
            skip_reason=reason,
        ),
        requested_mode=session_mode,
        requested_protocol=stream_protocol,
    )
    assert result.returncode == 0, result.stderr
    assert "run_status=skipped" in result.stdout
    assert "sample_eligible=false" in result.stdout


@pytest.mark.parametrize(
    ("session_mode", "stream_protocol", "reason"),
    [
        ("warm", "NCT1", "T0.4 only implements NCT1."),
        ("cold", "NCT2", "T0.4 only implements cold sessions."),
        ("warm", "NCT2", "T0.4 only implements NCT1."),
    ],
)
def test_skipped_run_rejects_reason_for_a_different_target(
    tmp_path, session_mode, stream_protocol, reason
):
    result = validate(
        tmp_path,
        manifest(
            run_status="skipped",
            session_mode=session_mode,
            stream_protocol=stream_protocol,
            skip_reason=reason,
        ),
        requested_mode=session_mode,
        requested_protocol=stream_protocol,
    )
    assert result.returncode != 0
    assert "skip_reason" in result.stderr
    assert "sample_eligible=true" not in result.stdout


def test_skipped_run_is_rejected_when_its_target_is_supported(tmp_path):
    result = validate(
        tmp_path,
        manifest(
            run_status="skipped",
            session_mode="cold",
            stream_protocol="NCT1",
            skip_reason="T0.4 only implements cold sessions.",
        ),
    )
    assert result.returncode != 0
    assert "skip_reason" in result.stderr


def test_non_skipped_run_rejects_a_not_testable_reason(tmp_path):
    result = validate(
        tmp_path,
        manifest(skip_reason="T0.4 only implements cold sessions."),
    )
    assert result.returncode != 0
    assert "skip_reason" in result.stderr


def test_failed_run_is_validly_recorded_but_not_a_success_sample(tmp_path):
    result = validate(tmp_path, manifest(run_status="failed"))
    assert result.returncode == 0, result.stderr
    assert "run_status=failed" in result.stdout
    assert "sample_eligible=false" in result.stdout


@pytest.mark.parametrize(
    ("field", "bad_value"),
    [
        ("plan_version", "3"),
        ("task_id", " "),
        ("candidate_id", ""),
        ("baseline_id", " "),
        ("pair_id", ""),
        ("session_mode", "Warm"),
        ("process_generation", 0),
        ("browser_reused", "false"),
        ("stream_protocol", "nct2"),
        ("build_profile", "debug"),
        ("run_status", "pass"),
        ("skip_reason", 3),
    ],
)
def test_invalid_type_empty_or_disallowed_value_is_rejected(tmp_path, field, bad_value):
    result = validate(tmp_path, manifest(**{field: bad_value}))
    assert result.returncode != 0
    assert field in result.stderr
    assert "sample_eligible=true" not in result.stdout


def test_pgo_requires_a_sha256_training_manifest_hash(tmp_path):
    result = validate(tmp_path, manifest(build_profile="release-pgo"))
    assert result.returncode != 0
    assert "training_manifest_hash" in result.stderr
    assert "when build_profile ends in -pgo" in result.stderr
    assert "for release-pgo" not in result.stderr


def test_pgo_accepts_a_sha256_training_manifest_hash(tmp_path):
    result = validate(tmp_path, manifest(build_profile="release-pgo", training_manifest_hash="a" * 64))
    assert result.returncode == 0, result.stderr
    assert "sample_eligible=true" in result.stdout


@pytest.mark.parametrize("profile", ["release-pgo", "release-thin-pgo", "release-thin-one-pgo"])
def test_each_pgo_profile_accepts_sha256_training_hash(tmp_path, profile):
    result = validate(tmp_path, manifest(build_profile=profile, training_manifest_hash="b" * 64))
    assert result.returncode == 0, result.stderr
    assert "sample_eligible=true" in result.stdout


@pytest.mark.parametrize("profile", ["release-pgo", "release-thin-pgo", "release-thin-one-pgo"])
@pytest.mark.parametrize("training_hash", [None, "not-a-sha256", "a" * 63, "a" * 65])
def test_each_pgo_profile_rejects_missing_or_invalid_training_hash(tmp_path, profile, training_hash):
    result = validate(tmp_path, manifest(build_profile=profile, training_manifest_hash=training_hash))
    assert result.returncode != 0
    assert "training_manifest_hash" in result.stderr


@pytest.mark.parametrize("profile", ["release", "release-thin", "release-thin-one"])
def test_each_base_profile_requires_null_training_hash(tmp_path, profile):
    result = validate(tmp_path, manifest(build_profile=profile, training_manifest_hash=None))
    assert result.returncode == 0, result.stderr
    assert "sample_eligible=true" in result.stdout


@pytest.mark.parametrize("profile", ["release", "release-thin", "release-thin-one"])
def test_each_base_profile_rejects_training_hash(tmp_path, profile):
    result = validate(tmp_path, manifest(build_profile=profile, training_manifest_hash="c" * 64))
    assert result.returncode != 0
    assert "training_manifest_hash" in result.stderr
    assert "base profiles without -pgo" in result.stderr


def test_non_pgo_profiles_require_null_training_manifest_hash(tmp_path):
    result = validate(tmp_path, manifest(training_manifest_hash="a" * 64))
    assert result.returncode != 0
    assert "training_manifest_hash" in result.stderr
