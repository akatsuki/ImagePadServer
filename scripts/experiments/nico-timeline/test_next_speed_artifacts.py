import hashlib
import json
import os
import shutil
import subprocess
from pathlib import Path

import pytest


MODULE = Path(__file__).with_name("next-speed-artifacts.psm1")
POWERSHELL = shutil.which("pwsh")


@pytest.fixture(autouse=True)
def require_powershell():
    if POWERSHELL is None:
        pytest.fail("pwsh is required to exercise the Windows artifact module")


def ps_quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def call_ps(body):
    command = f"Import-Module {ps_quote(MODULE)} -Force; {body}"
    return subprocess.run(
        [POWERSHELL, "-NoProfile", "-Command", command],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


def result_json(completed):
    for line in reversed(completed.stdout.splitlines()):
        if line.lstrip().startswith("{"):
            return json.loads(line)
    raise AssertionError(f"PowerShell returned no JSON result: {completed.stdout!r} {completed.stderr!r}")


def make_artifact(root, name="owned.bin", contents=b"fixture-owned-bytes"):
    root.mkdir(parents=True, exist_ok=True)
    path = root / name
    path.write_bytes(contents)
    return path


def entry(path, contents=None, *, size=None, digest=None):
    data = path.read_bytes() if contents is None else contents
    return {
        "path": str(path.absolute()),
        "size_bytes": len(data) if size is None else size,
        "sha256": hashlib.sha256(data).hexdigest() if digest is None else digest,
        "purpose": "pytest temporary fixture",
    }


def write_manifest(root, manifest_path, entries, *, trusted_ancestors=(), capacity_only=False):
    value = {"schema_version": 1, "root": str(root.absolute()), "artifacts": entries}
    if trusted_ancestors:
        value["trusted_ancestors"] = [str(path.absolute()) for path in trusted_ancestors]
    if capacity_only:
        value["capacity_only"] = True
    manifest_path.write_text(json.dumps(value), encoding="utf-8")
    return manifest_path


def validate_command(root, manifest_path, target=None):
    args = f"-Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)}"
    if target is not None:
        args += f" -ArtifactPath {ps_quote(target)}"
    return call_ps(f"$r = Test-NicoOwnedArtifacts {args}; $r | ConvertTo-Json -Compress -Depth 8")


def make_junction(link, target):
    result = call_ps(
        f"New-Item -ItemType Junction -Path {ps_quote(link)} -Target {ps_quote(target)} | Out-Null"
    )
    if result.returncode != 0:
        pytest.skip(f"could not create temporary junction fixture: {result.stderr}")


def test_capacity_reservation_accepts_full_fresh_combined_budget():
    result = call_ps(
        "$g=1GB; Test-NicoCapacityReservation -ExistingRawBytes 0 -ReserveRawBytes (12*$g) "
        "-RawVolumeId 'R:' -RawVolumeFreeBytes (44*$g) -ExistingBuildBytes 0 "
        "-ReserveBuildBytes (24*$g) -BuildVolumeId 'R:' -BuildVolumeFreeBytes (44*$g) "
        "| ConvertTo-Json -Compress -Depth 8"
    )
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["accepted"] is True
    assert report["aggregate_projected_bytes"] == 36 * 1024**3
    assert report["volume_checks"][0]["remaining_bytes"] == 8 * 1024**3


@pytest.mark.parametrize(
    ("arguments", "expected_reason"),
    [
        ("-ExistingRawBytes 0 -ReserveRawBytes (12*$g+1) -RawVolumeId 'R:' -RawVolumeFreeBytes (44*$g) -ExistingBuildBytes 0 -ReserveBuildBytes 0 -BuildVolumeId '' -BuildVolumeFreeBytes 0", "raw_budget_exceeded"),
        ("-ExistingRawBytes 0 -ReserveRawBytes 0 -RawVolumeId '' -RawVolumeFreeBytes 0 -ExistingBuildBytes 0 -ReserveBuildBytes (24*$g+1) -BuildVolumeId 'B:' -BuildVolumeFreeBytes (40*$g)", "build_budget_exceeded"),
        ("-ExistingRawBytes (12*$g) -ReserveRawBytes 0 -RawVolumeId '' -RawVolumeFreeBytes 0 -ExistingBuildBytes (24*$g) -ReserveBuildBytes 1 -BuildVolumeId 'B:' -BuildVolumeFreeBytes (40*$g)", "combined_budget_exceeded"),
        ("-ExistingRawBytes 0 -ReserveRawBytes $g -RawVolumeId 'R:' -RawVolumeFreeBytes (20*$g-1) -ExistingBuildBytes 0 -ReserveBuildBytes 0 -BuildVolumeId '' -BuildVolumeFreeBytes 0", "raw_start_free_below_minimum"),
        ("-ExistingRawBytes 0 -ReserveRawBytes (12*$g) -RawVolumeId 'R:' -RawVolumeFreeBytes (20*$g-1) -ExistingBuildBytes 0 -ReserveBuildBytes 0 -BuildVolumeId '' -BuildVolumeFreeBytes 0", "volume_free_below_8gib_after_reservation"),
        ("-ExistingRawBytes 0 -ReserveRawBytes $g -RawVolumeId 'R:' -RawVolumeFreeBytes (44*$g) -ExistingBuildBytes 0 -ReserveBuildBytes $g -BuildVolumeId 'R:' -BuildVolumeFreeBytes (44*$g-1)", "same_volume_free_space_disagrees"),
        ("-ExistingRawBytes 0 -ReserveRawBytes 0 -RawVolumeId '' -RawVolumeFreeBytes 0 -ExistingBuildBytes 0 -ReserveBuildBytes $g -BuildVolumeId 'B:' -BuildVolumeFreeBytes (40*$g-1)", "build_start_free_below_minimum"),
    ],
)
def test_capacity_reservation_rejects_limit_or_volume_shortfall(arguments, expected_reason):
    result = call_ps(f"$g=1GB; Test-NicoCapacityReservation {arguments} | ConvertTo-Json -Compress -Depth 8")
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["accepted"] is False
    assert expected_reason in report["rejection_codes"]


def test_manifest_validation_accepts_only_matching_owned_file(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root)
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact)])
    result = validate_command(root, manifest_path, artifact)
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["valid"] is True
    assert report["artifacts"][0]["path"] == str(artifact.resolve())
    assert report["total_bytes"] == artifact.stat().st_size


def test_capacity_only_manifest_hashes_hardlink_files_read_only(tmp_path):
    root = tmp_path / "capacity-run"
    target = make_artifact(tmp_path / "source", "build-output.bin", b"hardlinked-build-output")
    root.mkdir()
    linked = root / target.name
    try:
        os.link(target, linked)
    except (OSError, NotImplementedError) as exc:
        pytest.skip(f"hard-link fixture unavailable: {type(exc).__name__}: {exc}")
    manifest_path = write_manifest(
        root, tmp_path / "capacity-artifacts.json", [entry(linked)], capacity_only=True
    )

    result = validate_command(root, manifest_path)

    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["valid"] is True
    assert report["capacity_only"] is True
    assert report["total_bytes"] == len(b"hardlinked-build-output")
    assert report["artifacts"][0]["sha256"] == hashlib.sha256(target.read_bytes()).hexdigest()
    assert target.read_bytes() == linked.read_bytes()


def test_capacity_only_manifest_is_refused_by_cleanup_without_mutation(tmp_path):
    root = tmp_path / "capacity-run"
    target = make_artifact(tmp_path / "source", "build-output.bin", b"must-not-delete")
    root.mkdir()
    linked = root / target.name
    try:
        os.link(target, linked)
    except (OSError, NotImplementedError) as exc:
        pytest.skip(f"hard-link fixture unavailable: {type(exc).__name__}: {exc}")
    manifest_path = write_manifest(
        root, tmp_path / "capacity-artifacts.json", [entry(linked)], capacity_only=True
    )
    log_path = tmp_path / "capacity-cleanup.jsonl"
    target_before = target.read_bytes()
    linked_before = linked.read_bytes()
    manifest_before = manifest_path.read_bytes()

    result = call_ps(
        f"Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(linked)} -LogPath {ps_quote(log_path)} -Confirm:$false -WhatIf | Out-Null"
    )

    assert result.returncode != 0
    assert "capacity_only" in result.stderr.lower() or "read-only" in result.stderr.lower()
    assert target.read_bytes() == target_before
    assert linked.read_bytes() == linked_before
    assert manifest_path.read_bytes() == manifest_before
    assert not log_path.exists()


def test_non_capacity_manifest_still_rejects_hardlink_artifact(tmp_path):
    root = tmp_path / "ordinary-run"
    target = make_artifact(tmp_path / "source", "build-output.bin", b"ordinary-hardlink")
    root.mkdir()
    linked = root / target.name
    try:
        os.link(target, linked)
    except (OSError, NotImplementedError) as exc:
        pytest.skip(f"hard-link fixture unavailable: {type(exc).__name__}: {exc}")
    manifest_path = write_manifest(root, tmp_path / "ordinary-artifacts.json", [entry(linked)])

    result = validate_command(root, manifest_path)

    assert result.returncode != 0
    assert "reparse" in result.stderr.lower() or "hard" in result.stderr.lower() or "link" in result.stderr.lower()


def test_empty_owned_manifest_has_zero_total_bytes(tmp_path):
    root = tmp_path / "empty-run"
    root.mkdir()
    manifest_path = write_manifest(root, tmp_path / "empty-artifacts.json", [])
    result = validate_command(root, manifest_path)
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["valid"] is True
    assert report["artifacts"] == []
    assert report["total_bytes"] == 0


def test_manifest_rejects_null_trusted_ancestor_declaration(tmp_path):
    root = tmp_path / "null-trust-run"
    root.mkdir()
    manifest_path = tmp_path / "null-trust-artifacts.json"
    manifest_path.write_text(
        json.dumps({"schema_version": 1, "root": str(root.absolute()), "artifacts": [], "trusted_ancestors": None}),
        encoding="utf-8",
    )
    result = validate_command(root, manifest_path)
    assert result.returncode != 0
    assert "trusted_ancestors" in result.stderr


def test_explicit_trusted_reparse_ancestor_stays_within_owned_root(tmp_path):
    physical_parent = tmp_path / "physical-parent"
    root = physical_parent / "owned"
    artifact = make_artifact(root, contents=b"inside-trusted-boundary")
    sibling = physical_parent / "not-owned.txt"
    sibling.write_bytes(b"must remain outside declared root")
    alias = tmp_path / "trusted-parent"
    make_junction(alias, physical_parent)
    alias_root = alias / "owned"
    alias_artifact = alias_root / artifact.name
    manifest_path = write_manifest(
        alias_root,
        tmp_path / "owned-artifacts.json",
        [entry(alias_artifact)],
        trusted_ancestors=[alias],
    )

    result = validate_command(alias_root, manifest_path, alias_artifact)
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["total_bytes"] == len(b"inside-trusted-boundary")

    cleanup = call_ps(
        f"$r = Remove-NicoOwnedArtifacts -Root {ps_quote(alias_root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(alias_artifact)} -LogPath {ps_quote(tmp_path / 'trusted-cleanup.jsonl')} "
        f"-Confirm:$false; $r | ConvertTo-Json -Compress -Depth 8"
    )
    assert cleanup.returncode == 0, cleanup.stderr
    assert not artifact.exists()
    assert sibling.read_bytes() == b"must remain outside declared root"


def test_trusted_ancestor_cannot_allow_reparse_at_root_or_inside_root(tmp_path):
    physical_parent = tmp_path / "physical-parent"
    root = physical_parent / "owned"
    artifact = make_artifact(root, contents=b"inside-root")
    external = make_artifact(tmp_path / "external", "outside.bin", contents=b"outside-root")
    alias = tmp_path / "trusted-parent"
    make_junction(alias, physical_parent)
    alias_root = alias / "owned"

    root_link = tmp_path / "root-link"
    make_junction(root_link, root)
    root_manifest = write_manifest(
        root_link,
        tmp_path / "root-manifest.json",
        [entry(root_link / artifact.name)],
        trusted_ancestors=[root_link],
    )
    root_result = validate_command(root_link, root_manifest)
    assert root_result.returncode != 0

    inside_link = root / "external-link"
    make_junction(inside_link, external.parent)
    alias_external = alias_root / inside_link.name / external.name
    inside_manifest = write_manifest(
        alias_root,
        tmp_path / "inside-manifest.json",
        [entry(alias_external)],
        trusted_ancestors=[alias],
    )
    inside_result = validate_command(alias_root, inside_manifest)
    assert inside_result.returncode != 0
    assert external.read_bytes() == b"outside-root"


def test_manifest_rejects_root_external_reference(tmp_path):
    root = tmp_path / "run"
    root.mkdir()
    external = make_artifact(tmp_path / "outside", "outside.bin")
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(external)])
    result = validate_command(root, manifest_path)
    assert result.returncode != 0
    assert "outside" in result.stderr.lower() or "root" in result.stderr.lower()


def test_manifest_rejects_path_not_owned_by_manifest(tmp_path):
    root = tmp_path / "run"
    listed = make_artifact(root, "listed.bin")
    unlisted = make_artifact(root, "unlisted.bin")
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(listed)])
    result = validate_command(root, manifest_path, unlisted)
    assert result.returncode != 0
    assert "unowned" in result.stderr.lower()


def test_manifest_rejects_hash_or_size_mismatch(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root)
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact, digest="0" * 64)])
    result = validate_command(root, manifest_path)
    assert result.returncode != 0
    assert "hash" in result.stderr.lower() or "mismatch" in result.stderr.lower()


@pytest.mark.parametrize("bad_entries", ["duplicate", "relative"])
def test_manifest_rejects_duplicate_or_invalid_entries(tmp_path, bad_entries):
    root = tmp_path / "run"
    artifact = make_artifact(root)
    rows = [entry(artifact), entry(artifact)] if bad_entries == "duplicate" else [{**entry(artifact), "path": "owned.bin"}]
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", rows)
    result = validate_command(root, manifest_path)
    assert result.returncode != 0


def test_reparse_metadata_seam_detects_reparse_and_available_link_fixture(tmp_path):
    seam = call_ps(
        "$fake=[pscustomobject]@{Attributes=[System.IO.FileAttributes]::ReparsePoint;LinkType=$null;Target=$null}; "
        "Test-NicoReparseMetadata $fake | ConvertTo-Json -Compress"
    )
    assert seam.returncode == 0, seam.stderr
    assert seam.stdout.strip().lower() == "true"

    root = tmp_path / "run"
    root.mkdir()
    external = make_artifact(tmp_path / "outside", "target.bin")
    link = root / "link.bin"
    try:
        os.symlink(external, link)
    except (OSError, NotImplementedError):
        # The metadata seam above exercises the same fail-closed predicate when
        # this Windows account cannot create a reparse-point fixture.
        return
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(link)])
    result = validate_command(root, manifest_path)
    assert result.returncode != 0
    assert "reparse" in result.stderr.lower()


def test_ancestor_junction_is_rejected_and_external_target_is_preserved(tmp_path):
    root = tmp_path / "run"
    root.mkdir()
    external_dir = tmp_path / "external"
    external = make_artifact(external_dir, "target.bin", b"outside-root-data")
    junction = root / "linked"
    create = call_ps(
        f"New-Item -ItemType Junction -Path {ps_quote(junction)} -Target {ps_quote(external_dir)} "
        "-ErrorAction Stop | Out-Null"
    )
    if create.returncode != 0:
        pytest.skip(f"Windows junction fixture unavailable: {create.stderr.strip() or create.stdout.strip()}")
    try:
        linked = junction / "target.bin"
        manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(linked)])
        result = validate_command(root, manifest_path)
        assert result.returncode != 0
        assert "reparse" in result.stderr.lower()
        assert external.read_bytes() == b"outside-root-data"
    finally:
        call_ps(f"[System.IO.Directory]::Delete({ps_quote(junction)}, $false)")


def test_whatif_does_not_delete_or_claim_reclaimed_bytes(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"keep-this-fixture")
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact)])
    log_path = tmp_path / "delete-audit.jsonl"
    result = call_ps(
        f"$r=Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(artifact)} -LogPath {ps_quote(log_path)} -Confirm:$false -WhatIf; "
        "$r | ConvertTo-Json -Compress -Depth 8"
    )
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["status"] == "cancelled"
    assert report["reclaimed_bytes"] == 0
    assert artifact.read_bytes() == b"keep-this-fixture"


def test_remove_returns_exact_reclaimed_bytes_and_reinvocation_refuses_missing_file(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"exactly-17-bytes!")
    expected_bytes = artifact.stat().st_size
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact)])
    log_path = tmp_path / "delete-audit.jsonl"
    command = (
        f"$r=Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(artifact)} -LogPath {ps_quote(log_path)} -Confirm:$false; "
        "$r | ConvertTo-Json -Compress -Depth 8"
    )
    result = call_ps(command)
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["status"] == "completed"
    assert report["reclaimed_bytes"] == expected_bytes
    assert not artifact.exists()
    assert json.loads(log_path.read_text(encoding="utf-8").splitlines()[0])["sha256"] == entry_from_json(manifest_path, 0)["sha256"]

    second = call_ps(command)
    assert second.returncode != 0
    assert not artifact.exists()


def test_handle_delete_rejects_stale_manifest_hash_and_size_without_changing_file(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"preserve-on-stale-manifest")
    before = artifact.read_bytes()
    result = call_ps(
        f"try {{ [NicoVerifiedArtifactDelete]::DeleteIfUnchanged({ps_quote(artifact)}, {ps_quote(root)}, "
        f"{len(before) + 1}, '{'0' * 64}') | Out-Null; 'unexpected_success'; exit 3 }} "
        "catch { 'rejected=' + $_.Exception.Message }"
    )
    assert result.returncode == 0, result.stderr
    assert "rejected=" in result.stdout
    assert "unexpected_success" not in result.stdout
    assert artifact.read_bytes() == before


def test_remove_rejects_log_path_aliasing_manifest_without_mutation(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"remain-owned")
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact)])
    manifest_alias = manifest_path.parent / "normalization-probe" / ".." / manifest_path.name
    manifest_before = manifest_path.read_bytes()
    artifact_before = artifact.read_bytes()
    result = call_ps(
        f"Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(artifact)} -LogPath {ps_quote(manifest_alias)} -Confirm:$false -WhatIf | Out-Null"
    )
    assert result.returncode != 0
    assert manifest_path.read_bytes() == manifest_before
    assert artifact.read_bytes() == artifact_before


def test_remove_rejects_existing_hardlink_log_alias_without_mutation(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"hardlink-alias-protected")
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact)])
    alias = tmp_path / "audit-alias.jsonl"
    try:
        os.link(manifest_path, alias)
    except (OSError, NotImplementedError) as exc:
        pytest.skip(f"hard-link fixture unavailable: {type(exc).__name__}: {exc}")
    manifest_before = manifest_path.read_bytes()
    artifact_before = artifact.read_bytes()
    result = call_ps(
        f"Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(artifact)} -LogPath {ps_quote(alias)} -Confirm:$false -WhatIf | Out-Null"
    )
    assert result.returncode != 0
    assert "fresh" in result.stderr.lower()
    assert manifest_path.read_bytes() == manifest_before
    assert artifact.read_bytes() == artifact_before


def test_remove_requires_fresh_nonexistent_log_path(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"fresh-log-protected")
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact)])
    log_path = tmp_path / "already-used.jsonl"
    log_path.write_bytes(b"existing-log-content")
    log_before = log_path.read_bytes()
    manifest_before = manifest_path.read_bytes()
    artifact_before = artifact.read_bytes()
    result = call_ps(
        f"Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath {ps_quote(artifact)} -LogPath {ps_quote(log_path)} -Confirm:$false -WhatIf | Out-Null"
    )
    assert result.returncode != 0
    assert log_path.read_bytes() == log_before
    assert manifest_path.read_bytes() == manifest_before
    assert artifact.read_bytes() == artifact_before


def test_handle_delete_rejects_hard_link_without_deleting_either_name(tmp_path):
    root = tmp_path / "run"
    target = make_artifact(root, "target.bin", b"shared-hard-link-data")
    alias = root / "alias.bin"
    try:
        os.link(target, alias)
    except (OSError, NotImplementedError) as exc:
        pytest.skip(f"hard-link fixture unavailable: {type(exc).__name__}: {exc}")
    before = target.read_bytes()
    result = call_ps(
        f"try {{ [NicoVerifiedArtifactDelete]::DeleteIfUnchanged({ps_quote(target)}, {ps_quote(root)}, "
        f"{len(before)}, '{hashlib.sha256(before).hexdigest()}') | Out-Null; 'unexpected_success'; exit 3 }} "
        "catch { 'rejected=' + $_.Exception.Message }"
    )
    assert result.returncode == 0, result.stderr
    assert "rejected=" in result.stdout
    assert "unexpected_success" not in result.stdout
    assert target.read_bytes() == before
    assert alias.read_bytes() == before


def test_deleted_artifact_is_not_reported_as_delete_failure_when_audit_append_fails(tmp_path):
    root = tmp_path / "run"
    artifact = make_artifact(root, contents=b"deleted-before-audit-error")
    remaining = make_artifact(root, "remaining.bin", b"must-remain-after-audit-error")
    remaining_hash = hashlib.sha256(remaining.read_bytes()).hexdigest()
    size = artifact.stat().st_size
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(artifact), entry(remaining)])
    log_path = tmp_path / "delete-audit.jsonl"
    body = (
        "$writer={ param($path,$record) if ($record.event -eq 'deleted') { throw 'injected audit append failure' }; "
        "[System.IO.File]::AppendAllText($path,(ConvertTo-Json -InputObject $record -Compress)+[Environment]::NewLine) }; "
        f"$r=Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath @({ps_quote(artifact)},{ps_quote(remaining)}) -LogPath {ps_quote(log_path)} -AuditWriter $writer -Confirm:$false; "
        "$r | ConvertTo-Json -Compress -Depth 8"
    )
    result = call_ps(body)
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["status"] == "partial_audit_failure"
    assert report["reclaimed_bytes"] == size
    assert report["failed_paths"] == []
    assert len(report["audit_failures"]) == 1
    assert not artifact.exists()
    assert remaining.exists()
    assert hashlib.sha256(remaining.read_bytes()).hexdigest() == remaining_hash


def entry_from_json(manifest_path, index):
    return json.loads(manifest_path.read_text(encoding="utf-8"))["artifacts"][index]


def test_partial_failure_reports_only_bytes_already_deleted(tmp_path):
    root = tmp_path / "run"
    first = make_artifact(root, "a.bin", b"delete-me")
    second = make_artifact(root, "b.bin", b"cannot-delete-now")
    second_hash = hashlib.sha256(second.read_bytes()).hexdigest()
    manifest_path = write_manifest(root, tmp_path / "artifacts.json", [entry(first), entry(second)])
    log_path = tmp_path / "delete-audit.jsonl"
    body = (
        f"$lock=[System.IO.File]::Open({ps_quote(second)},[System.IO.FileMode]::Open,[System.IO.FileAccess]::Read,[System.IO.FileShare]::Read); "
        f"try {{ $r=Remove-NicoOwnedArtifacts -Root {ps_quote(root)} -ManifestPath {ps_quote(manifest_path)} "
        f"-ArtifactPath @({ps_quote(first)},{ps_quote(second)}) -LogPath {ps_quote(log_path)} -Confirm:$false; "
        "$r | ConvertTo-Json -Compress -Depth 8 } finally { $lock.Dispose() }"
    )
    result = call_ps(body)
    assert result.returncode == 0, result.stderr
    report = result_json(result)
    assert report["status"] == "partial_failure"
    assert report["reclaimed_bytes"] == len(b"delete-me")
    assert not first.exists()
    assert second.exists()
    assert hashlib.sha256(second.read_bytes()).hexdigest() == second_hash
    audit = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
    assert [item["event"] for item in audit] == [
        "delete_prepared",
        "deleted",
        "delete_prepared",
        "delete_failed",
    ]
