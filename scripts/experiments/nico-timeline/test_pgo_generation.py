"""Mocked end-to-end contract tests for T9.2 PGO profile generation.

All commands are temporary PowerShell shims. These tests never invoke a real
Cargo build, compositor, GPU, or LLVM toolchain.
"""

import hashlib
import json
import os
import shutil
import subprocess
from pathlib import Path

import pytest


SCRIPT = Path(__file__).with_name("pgo.ps1")
POWERSHELL = shutil.which("pwsh")
TARGET = "x86_64-pc-windows-msvc"
TEMP_ROOT = Path(os.environ.get("TEMP", os.environ.get("TMP", "."))).resolve()
OWNED_RUN_ROOTS = []


def sha(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def manifests(*, overlap=False):
    def material(label, coverage):
        return {
            "id": label,
            "coverage": coverage,
            "sourceSha256": sha("source:" + label),
            "snapshotSha256": sha("snapshot:" + label),
            "seed": 17,
        }

    training = {
        "schemaVersion": 1,
        "role": "training",
        "materials": [
            material("train-short", "short"),
            material("train-long", "long"),
            material("train-dense", "dense"),
            material("train-nct1", "NCT1"),
            material("train-nct2", "NCT2"),
        ],
    }
    evaluation_materials = [
        material("eval-short", "short"),
        material("eval-long", "long"),
        material("eval-dense", "dense"),
        material("eval-nct1", "NCT1"),
        material("eval-nct2", "NCT2"),
    ]
    if overlap:
        # Identical source/snapshot with a different seed is still overlap.
        evaluation_materials[0]["sourceSha256"] = training["materials"][0]["sourceSha256"]
        evaluation_materials[0]["snapshotSha256"] = training["materials"][0]["snapshotSha256"]
        evaluation_materials[0]["seed"] = 999
    evaluation = {
        "schemaVersion": 1,
        "role": "evaluation",
        "materials": evaluation_materials,
    }
    return training, evaluation


def write_json(path: Path, value):
    path.write_text(json.dumps(value, indent=2), encoding="utf-8")


def write_workload_catalog(tmp_path: Path, training, evaluation):
    inputs = tmp_path / "workload-inputs"
    inputs.mkdir()
    browser = inputs / "chrome.exe"
    browser.write_bytes(b"mock browser executable")
    training_pairs = {}
    materials_by_role = {}
    for role, manifest in (("training", training), ("evaluation", evaluation)):
        records = []
        for material in manifest["materials"]:
            key = (material["sourceSha256"], material["snapshotSha256"])
            shared = training_pairs.get(key) if role == "evaluation" else None
            if shared is not None:
                source, snapshot = shared
            else:
                source = inputs / f"{role}-{material['id']}.mp4"
                snapshot = inputs / f"{role}-{material['id']}.json"
                source.write_bytes(("source:" + role + ":" + material["id"]).encode())
                snapshot.write_bytes(("snapshot:" + role + ":" + material["id"]).encode())
                if role == "training":
                    training_pairs[key] = (source, snapshot)

            source_sha = hashlib.sha256(source.read_bytes()).hexdigest()
            snapshot_sha = hashlib.sha256(snapshot.read_bytes()).hexdigest()
            # Preserve intentionally malformed manifest values so input validation
            # tests still exercise the manifest rejection path.
            if len(material["sourceSha256"]) == 64:
                material["sourceSha256"] = source_sha
            if len(material["snapshotSha256"]) == 64:
                material["snapshotSha256"] = snapshot_sha
            records.append(
                {
                    "id": material["id"],
                    "coverage": material["coverage"],
                    "sourcePath": str(source.resolve()),
                    "snapshotPath": str(snapshot.resolve()),
                    "protocol": "NCT1" if material["coverage"] == "NCT1" else "NCT2",
                    "width": 1280,
                    "height": 720,
                    "durationMs": 6000,
                    "fpsNum": 30,
                    "fpsDen": 1,
                    "backend": "auto",
                    "readbackSlots": 3,
                    "assetLayout": "separate",
                }
            )
        materials_by_role[role] = records
    catalog = {
        "schemaVersion": 1,
        "browserPath": str(browser.resolve()),
        "training": materials_by_role["training"],
        "evaluation": materials_by_role["evaluation"],
    }
    path = tmp_path / "workload-catalog.json"
    write_json(path, catalog)
    return path, catalog


def write_shims(tmp_path: Path):
    """Create deterministic tool shims which record calls and emulate outputs."""
    cargo = tmp_path / "cargo-shim.ps1"
    cargo.write_text(
        r'''
param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Rest)
if ($Rest.Count -gt 0 -and $Rest[0] -eq '--version') { Write-Output 'cargo 1.90.0 (mock)'; exit 0 }
$record = [ordered]@{ args = @($Rest); exitCode = [int]$env:MOCK_CARGO_EXIT }
$record | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $env:MOCK_CARGO_RECORD -Encoding utf8
if ($record.exitCode -ne 0) { exit $record.exitCode }
$targetDirIndex = [Array]::IndexOf($Rest, '--target-dir')
$targetIndex = [Array]::IndexOf($Rest, '--target')
$profileIndex = [Array]::IndexOf($Rest, '--profile')
if ($targetDirIndex -lt 0 -or $targetIndex -lt 0 -or $profileIndex -lt 0) { exit 86 }
$binaryDir = Join-Path $Rest[$targetDirIndex + 1] (Join-Path $Rest[$targetIndex + 1] $Rest[$profileIndex + 1])
[System.IO.Directory]::CreateDirectory($binaryDir) | Out-Null
[System.IO.File]::WriteAllBytes((Join-Path $binaryDir 'nico-compositord.exe'), [byte[]](7,7,7,7,7,7))
'''.strip(),
        encoding="utf-8",
    )
    trainer = tmp_path / "trainer-shim.ps1"
    trainer.write_text(
        r'''
param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Rest)
$record = [ordered]@{
  args = @($Rest)
  helper = $env:NICO_TIMELINE_WORKER_HELPER
  trainingManifest = $env:NICO_PGO_TRAINING_MANIFEST
  evaluationManifest = $env:NICO_PGO_EVALUATION_MANIFEST
  workloadCatalog = $env:NICO_PGO_WORKLOAD_CATALOG
  profilePattern = $env:LLVM_PROFILE_FILE
}
$record | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $env:MOCK_TRAINER_RECORD -Encoding utf8
if ($record.profilePattern) {
  $rawPath = $record.profilePattern.Replace('%p', '4242').Replace('%m', 'nico-compositord')
  [System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($rawPath)) | Out-Null
  [System.IO.File]::WriteAllBytes($rawPath, [byte[]](1,2,3,4,5,6))
}
if ([int]$env:MOCK_TRAINER_EXIT -ne 0) { exit ([int]$env:MOCK_TRAINER_EXIT) }
$training = Get-Content -LiteralPath $record.trainingManifest -Raw -Encoding UTF8 | ConvertFrom-Json
$catalog = Get-Content -LiteralPath $record.workloadCatalog -Raw -Encoding UTF8 | ConvertFrom-Json
$workloads = Join-Path $env:NICO_PGO_RUN_ROOT 'workloads'
[System.IO.Directory]::CreateDirectory($workloads) | Out-Null
$runs = @()
foreach ($material in $training.materials) {
  $workloadInput = $catalog.training | Where-Object { $_.id -ceq $material.id } | Select-Object -First 1
  $scene = Join-Path $workloads ($material.id + $(if ($workloadInput.protocol -ceq 'NCT1') { '.nct' } else { '.nct2' }))
  [System.IO.File]::WriteAllBytes($scene, [byte[]](4,3,2,1))
  $sceneHash = (Get-FileHash -LiteralPath $scene -Algorithm SHA256).Hash.ToLowerInvariant()
  $runtimePath = Join-Path $workloads ($material.id + '-runtime.json')
  $runtime = [ordered]@{
    protocol = $workloadInput.protocol; renderer = 'wgpu'; version = 'mock'; requestedBackend = $workloadInput.backend
    backend = 'dx12'; adapterName = 'mock-adapter'; readbackSlots = $workloadInput.readbackSlots
    requestedAssetLayout = $workloadInput.assetLayout; assetLayout = $workloadInput.assetLayout; completedFrames = 180; error = $null
  }
  $runtime | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $runtimePath -Encoding utf8
  $runs += [ordered]@{
    id = $material.id; coverage = $material.coverage; protocol = $workloadInput.protocol
    width = $workloadInput.width; height = $workloadInput.height; durationMs = $workloadInput.durationMs
    fpsNum = $workloadInput.fpsNum; fpsDen = $workloadInput.fpsDen; backend = $workloadInput.backend
    readbackSlots = $workloadInput.readbackSlots; assetLayout = $workloadInput.assetLayout
    sourcePath = $workloadInput.sourcePath; sourceSha256 = $material.sourceSha256
    snapshotPath = $workloadInput.snapshotPath; snapshotSha256 = $material.snapshotSha256
    scenePath = $scene; sceneBytes = 4; sceneSha256 = $sceneHash
    captureFrameCount = 180; captureEligibleComments = 1; captureAssets = 1; captureDraws = 1
    runtimeReportPath = $runtimePath; runtimeReport = $runtime
    wallSeconds = 0.01
  }
}
$evidence = [ordered]@{ schemaVersion = 1; helperPath = $record.helper; helperSha256 = (Get-FileHash -LiteralPath $record.helper -Algorithm SHA256).Hash.ToLowerInvariant(); runs = $runs }
if ($env:MOCK_BAD_WORKLOAD_EVIDENCE -eq 'helper-hash') { $evidence.helperSha256 = ('0' * 64) }
$evidence | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $env:NICO_PGO_RUN_ROOT 'pgo-workload-runs.json') -Encoding utf8
'''.strip(),
        encoding="utf-8",
    )
    llvm = tmp_path / "llvm-profdata-shim.ps1"
    llvm.write_text(
        r'''
param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Rest)
if ($Rest.Count -gt 0 -and $Rest[0] -eq '--version') { Write-Output ('LLVM version ' + $env:MOCK_LLVM_VERSION); exit 0 }
$record = [ordered]@{ args = @($Rest); exitCode = [int]$env:MOCK_LLVM_EXIT }
$record | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $env:MOCK_LLVM_RECORD -Encoding utf8
if ($record.exitCode -ne 0) { exit $record.exitCode }
$outputIndex = [Array]::IndexOf($Rest, '--output')
if ($outputIndex -lt 0 -or $outputIndex + 1 -ge $Rest.Count) { exit 87 }
$outputPath = $Rest[$outputIndex + 1]
[System.IO.File]::WriteAllBytes($outputPath, [byte[]](9,8,7,6,5,4,3,2,1))
'''.strip(),
        encoding="utf-8",
    )
    rustc = tmp_path / "rustc-shim.ps1"
    rustc.write_text(
        r'''
param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Rest)
if ($Rest.Count -gt 0 -and $Rest[0] -eq '-vV') {
  Write-Output 'rustc 1.90.0 (mock)'
  Write-Output 'host: x86_64-pc-windows-msvc'
  Write-Output ('LLVM version: ' + $env:MOCK_RUSTC_LLVM)
  exit 0
}
exit 88
'''.strip(),
        encoding="utf-8",
    )
    return cargo, trainer, llvm, rustc


def run_training(tmp_path: Path, *, training=None, evaluation=None, cargo_exit=0,
                 trainer_exit=0, llvm_exit=0, run_root=None,
                 rustc_llvm="22.0.0", llvm_version="22.0.0",
                 include_workload_catalog=True, corrupt_workload_input=False,
                 bad_workload_evidence=False):
    assert SCRIPT.is_file(), "pgo.ps1 entry point must exist before exercising training mode"
    assert POWERSHELL is not None, "pwsh is required for mocked PGO runner tests"
    training, evaluation = training or manifests()[0], evaluation or manifests()[1]
    workload_catalog_path, workload_catalog = write_workload_catalog(tmp_path, training, evaluation)
    # The PGO runner creates profileMetadata only after it has merged a real
    # profile. Input evaluation manifests contain held-out material identity.
    evaluation.pop("profileMetadata", None)
    training_path = tmp_path / "input-training.json"
    evaluation_path = tmp_path / "input-evaluation.json"
    write_json(training_path, training)
    write_json(evaluation_path, evaluation)
    cargo, trainer, llvm, rustc = write_shims(tmp_path)
    run_root = run_root or Path(os.environ.get("TEMP", str(tmp_path))) / ("nct-pgo-test-" + next(iter([sha(str(tmp_path))[:16]])))
    if run_root.resolve() != SCRIPT.parent.resolve():
        run_root = run_root.resolve()
        run_root.relative_to(TEMP_ROOT)
        assert not run_root.exists(), f"test RunRoot must be fresh: {run_root}"
        OWNED_RUN_ROOTS.append(run_root)
    env = os.environ.copy()
    env.update(
        {
            "MOCK_CARGO_EXIT": str(cargo_exit),
            "MOCK_TRAINER_EXIT": str(trainer_exit),
            "MOCK_LLVM_EXIT": str(llvm_exit),
            "MOCK_RUSTC_LLVM": rustc_llvm,
            "MOCK_LLVM_VERSION": llvm_version,
            "MOCK_BAD_WORKLOAD_EVIDENCE": "helper-hash" if bad_workload_evidence else "",
            "MOCK_CARGO_RECORD": str(tmp_path / "cargo-call.json"),
            "MOCK_TRAINER_RECORD": str(tmp_path / "trainer-call.json"),
            "MOCK_LLVM_RECORD": str(tmp_path / "llvm-call.json"),
        }
    )
    if corrupt_workload_input:
        Path(workload_catalog["training"][0]["sourcePath"]).write_bytes(b"changed after catalog hashing")
    arguments = [
        POWERSHELL,
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-File",
        str(SCRIPT),
        "-RunTraining",
        "-Mode",
        "release",
        "-Target",
        TARGET,
        "-RunRoot",
        str(run_root),
        "-TrainingManifest",
        str(training_path),
        "-EvaluationManifest",
        str(evaluation_path),
    ]
    if include_workload_catalog:
        arguments.extend(["-WorkloadCatalog", str(workload_catalog_path)])
    arguments.extend([
        "-TrainingRunner",
        str(trainer),
        "-CargoExecutable",
        str(cargo),
        "-LlvmProfdataExecutable",
        str(llvm),
        "-RustcExecutable",
        str(rustc),
        "-PythonExecutable",
        shutil.which("python") or "python",
        "-TrainingRunnerArguments",
        "fixture-training",
    ])
    result = subprocess.run(
        arguments,
        capture_output=True,
        text=True,
        check=False,
        timeout=60,
        env=env,
    )
    return result, run_root, env


@pytest.fixture(autouse=True)
def require_powershell():
    if POWERSHELL is None:
        pytest.fail("pwsh is required to exercise the mocked PGO runner")
    roots_before = len(OWNED_RUN_ROOTS)
    yield
    cleanup_script = r'''
$ErrorActionPreference = 'Stop'
$temp = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd('\')
$root = [System.IO.Path]::GetFullPath($env:NICO_TEST_OWNED_ROOT).TrimEnd('\')
$prefix = $temp + '\'
if ($root -eq $temp -or -not $root.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
  throw "Refusing test cleanup outside TEMP: $root"
}
$item = Get-Item -LiteralPath $root -Force -ErrorAction Stop
if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
  throw "Refusing test cleanup of reparse point: $root"
}
$ownerPath = Join-Path $root 'pgo-owner.json'
if (-not (Test-Path -LiteralPath $ownerPath -PathType Leaf)) { throw "Missing PGO owner marker: $root" }
$owner = Get-Content -LiteralPath $ownerPath -Raw -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
if ($owner.kind -cne 'nico-pgo-training' -or
    [System.IO.Path]::GetFullPath([string]$owner.runRoot).TrimEnd('\') -cne $root) {
  throw "PGO owner marker does not match test root: $root"
}
$stack = [System.Collections.Generic.Stack[string]]::new()
$stack.Push($root)
while ($stack.Count -gt 0) {
  $directory = $stack.Pop()
  foreach ($entry in Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop) {
    $entryPath = [System.IO.Path]::GetFullPath($entry.FullName)
    if (-not $entryPath.StartsWith(($root + '\'), [System.StringComparison]::OrdinalIgnoreCase)) {
      throw "Test output escaped its PGO root: $entryPath"
    }
    if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "Refusing test cleanup of reparse point: $entryPath"
    }
    if ($entry.PSIsContainer) { $stack.Push($entryPath) }
  }
}
Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction Stop
if (Test-Path -LiteralPath $root) { throw "Test cleanup did not remove PGO root: $root" }
'''
    for root in OWNED_RUN_ROOTS[roots_before:]:
        resolved_root = root.resolve()
        resolved_root.relative_to(TEMP_ROOT)
        if not resolved_root.exists():
            continue
        cleanup_env = os.environ.copy()
        cleanup_env["NICO_TEST_OWNED_ROOT"] = str(resolved_root)
        cleanup = subprocess.run(
            [POWERSHELL, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", cleanup_script],
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
            env=cleanup_env,
        )
        assert cleanup.returncode == 0, cleanup.stdout + cleanup.stderr
        assert not resolved_root.exists()
    del OWNED_RUN_ROOTS[roots_before:]


def test_training_mode_generates_profile_and_validated_provenance_under_owned_root(tmp_path):
    result, run_root, env = run_training(tmp_path)
    assert result.returncode == 0, result.stdout + result.stderr
    assert (run_root / "pgo-owner.json").is_file()
    assert (run_root / "training.json").is_file()
    assert (run_root / "evaluation.json").is_file()
    assert (run_root / "workload-catalog.json").is_file()
    assert (run_root / "workload-catalog-input.json").is_file()
    assert (run_root / "pgo-workload-runs.json").is_file()
    assert (run_root / "profile.profdata").is_file()
    assert (run_root / "pgo-result.json").is_file()

    owner = json.loads((run_root / "pgo-owner.json").read_text(encoding="utf-8-sig"))
    assert owner["status"] == "complete"
    assert owner["stage"] == "complete"
    catalog_path = run_root / "workload-catalog.json"
    catalog = json.loads(catalog_path.read_text(encoding="utf-8-sig"))
    catalog_files = [
        Path(item[field]).resolve()
        for role in ("training", "evaluation")
        for item in catalog[role]
        for field in ("sourcePath", "snapshotPath")
    ]
    assert all(path.is_relative_to(run_root.resolve()) for path in catalog_files)
    assert (run_root / "inputs").is_dir()
    assert owner["workloadCatalog"]["path"] == str(catalog_path)
    assert owner["workloadCatalog"]["sha256"] == hashlib.sha256(catalog_path.read_bytes()).hexdigest()
    assert owner["workloadCatalog"]["suppliedCatalogSha256"] == hashlib.sha256(
        (run_root / "workload-catalog-input.json").read_bytes()
    ).hexdigest()
    assert owner["workloadCatalog"]["inputCopies"]["files"] > 0
    evidence_path = run_root / "pgo-workload-runs.json"
    assert owner["workloadEvidence"]["path"] == str(evidence_path)
    assert owner["workloadEvidence"]["sha256"] == hashlib.sha256(evidence_path.read_bytes()).hexdigest()
    assert owner["workloadEvidence"]["runCount"] == 5
    profile = run_root / "profile.profdata"
    profile_sha = hashlib.sha256(profile.read_bytes()).hexdigest()
    assert profile.stat().st_size > 0
    output_eval = json.loads((run_root / "evaluation.json").read_text(encoding="utf-8-sig"))
    assert output_eval["profileMetadata"]["profileSha256"] == profile_sha
    assert output_eval["profileMetadata"]["instrumented"] is False

    # The generated evaluation manifest must pass the existing strict validator.
    validation = subprocess.run(
        [POWERSHELL, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", str(SCRIPT),
         "-ValidateManifests", "-TrainingManifest", str(run_root / "training.json"),
         "-EvaluationManifest", str(run_root / "evaluation.json")],
        capture_output=True, text=True, check=False, timeout=30,
    )
    assert validation.returncode == 0, validation.stdout + validation.stderr
    assert "manifests_valid=true" in validation.stdout

    cargo_call = json.loads(Path(env["MOCK_CARGO_RECORD"]).read_text(encoding="utf-8-sig"))
    cargo_args = cargo_call["args"]
    assert any("profile-generate" in item for item in cargo_args)
    target_dirs = [cargo_args[i + 1] for i, item in enumerate(cargo_args[:-1]) if item == "--target-dir"]
    assert target_dirs and all(Path(path).resolve().is_relative_to(run_root.resolve()) for path in target_dirs)

    trainer_call = json.loads(Path(env["MOCK_TRAINER_RECORD"]).read_text(encoding="utf-8-sig"))
    assert trainer_call["args"] == ["fixture-training"]
    assert trainer_call["helper"]
    assert Path(trainer_call["helper"]).resolve().is_relative_to(run_root.resolve())
    assert trainer_call["trainingManifest"] == str(run_root / "training.json")
    assert trainer_call["evaluationManifest"] == str(run_root / "evaluation.json")
    assert trainer_call["workloadCatalog"] == str(run_root / "workload-catalog.json")
    assert trainer_call["profilePattern"]
    profile_parent = Path(trainer_call["profilePattern"].replace("%p", "4242").replace("%m", "nico"))
    assert profile_parent.resolve().is_relative_to(run_root.resolve())

    llvm_call = json.loads(Path(env["MOCK_LLVM_RECORD"]).read_text(encoding="utf-8-sig"))
    assert "merge" in llvm_call["args"]
    output_index = llvm_call["args"].index("--output")
    assert Path(llvm_call["args"][output_index + 1]).resolve().is_relative_to(run_root.resolve())
    profraw_inputs = [item for item in llvm_call["args"] if item.endswith(".profraw")]
    assert profraw_inputs
    assert all(Path(path).resolve().is_relative_to(run_root.resolve()) for path in profraw_inputs)


def test_training_requires_real_workload_catalog_before_cargo_launch(tmp_path):
    result, run_root, env = run_training(tmp_path, include_workload_catalog=False)
    assert result.returncode != 0
    assert "workloadcatalog" in (result.stdout + result.stderr).lower()
    assert not Path(env["MOCK_CARGO_RECORD"]).exists()
    assert not (run_root / "pgo-result.json").exists()


def test_training_rejects_workload_input_hash_drift_before_cargo_launch(tmp_path):
    result, run_root, env = run_training(tmp_path, corrupt_workload_input=True)
    assert result.returncode != 0
    assert "sha-256" in (result.stdout + result.stderr).lower()
    assert not Path(env["MOCK_CARGO_RECORD"]).exists()
    assert not (run_root / "pgo-result.json").exists()


def test_training_rejects_instrumented_helper_identity_mismatch_in_workload_evidence(tmp_path):
    result, run_root, env = run_training(tmp_path, bad_workload_evidence=True)
    assert result.returncode != 0
    assert "helper hash does not match" in (result.stdout + result.stderr).lower()
    owner = json.loads((run_root / "pgo-owner.json").read_text(encoding="utf-8-sig"))
    assert owner["status"] == "failed"
    assert owner["stage"] == "validate-workload-evidence"
    assert not (run_root / "pgo-result.json").exists()
    assert not list(run_root.rglob("*.profraw"))


@pytest.mark.parametrize(
    ("stage", "cargo_exit", "trainer_exit", "llvm_exit"),
    [("cargo", 17, 0, 0), ("training", 0, 23, 0), ("cancel", 0, 130, 0), ("merge", 0, 0, 29)],
)
def test_failed_or_cancelled_stage_records_failure_cleans_intermediates_and_has_no_success_marker(
    tmp_path, stage, cargo_exit, trainer_exit, llvm_exit
):
    result, run_root, env = run_training(
        tmp_path, cargo_exit=cargo_exit, trainer_exit=trainer_exit, llvm_exit=llvm_exit
    )
    assert result.returncode != 0, result.stdout + result.stderr
    assert "stage" in (result.stdout + result.stderr).lower() or stage in (result.stdout + result.stderr).lower()
    assert (run_root / "pgo-owner.json").is_file()
    owner = json.loads((run_root / "pgo-owner.json").read_text(encoding="utf-8-sig"))
    assert owner["status"] == "failed"
    assert owner["stage"] == stage
    assert not (run_root / "pgo-result.json").exists()
    assert not list(run_root.rglob("*.profraw"))
    assert not (run_root / "cargo-target").exists()


@pytest.mark.parametrize("overlap", [False, True])
def test_invalid_or_overlapping_manifests_fail_before_cargo_launch(tmp_path, overlap):
    training, evaluation = manifests(overlap=overlap)
    if not overlap:
        training["materials"][0]["sourceSha256"] = "not-a-sha256"
    result, run_root, env = run_training(tmp_path, training=training, evaluation=evaluation)
    assert result.returncode != 0
    assert not Path(env["MOCK_CARGO_RECORD"]).exists(), "manifest rejection must precede cargo"
    assert not (run_root / "pgo-result.json").exists()
    assert "manifest" in (result.stdout + result.stderr).lower()


def test_run_root_outside_temp_is_rejected_without_writing_into_the_checkout(tmp_path):
    result, run_root, env = run_training(tmp_path, run_root=SCRIPT.parent)
    assert result.returncode != 0
    assert "temp" in (result.stdout + result.stderr).lower()
    assert not Path(env["MOCK_CARGO_RECORD"]).exists()
    assert not (SCRIPT.parent / "pgo-owner.json").exists()


def test_llvm_version_mismatch_is_recorded_before_instrumented_cargo_build(tmp_path):
    result, run_root, env = run_training(tmp_path, rustc_llvm="22.0.0", llvm_version="21.1.0")
    assert result.returncode != 0
    assert "llvm-profdata llvm 21.1.0 does not match rustc llvm 22.0.0" in (
        result.stdout + result.stderr
    ).lower()
    assert not Path(env["MOCK_CARGO_RECORD"]).exists()
    owner = json.loads((run_root / "pgo-owner.json").read_text(encoding="utf-8-sig"))
    assert owner["status"] == "failed"
    assert owner["stage"] == "toolchain"
