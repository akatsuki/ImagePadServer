import json
import hashlib
import os
import signal
import shutil
import subprocess
import ctypes
import time
from pathlib import Path

import pytest


SCRIPT = Path(__file__).with_name("next-speed-benchmark.ps1")
POWERSHELL = shutil.which("pwsh")


@pytest.fixture(autouse=True)
def require_powershell():
    if POWERSHELL is None:
        pytest.fail("pwsh is required to exercise the cold-case runner")


def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def ps_quote(value) -> str:
    return "'" + str(value).replace("'", "''") + "'"


def owned_manifest(tmp_path: Path, name: str, payload: bytes) -> Path:
    root = tmp_path / f"{name}-owned"
    root.mkdir()
    artifact = root / f"{name}.bin"
    artifact.write_bytes(payload)
    value = {
        "schema_version": 1,
        "root": str(root),
        "artifacts": [{
            "path": str(artifact),
            "size_bytes": artifact.stat().st_size,
            "sha256": sha(artifact),
            "purpose": f"fixture {name} reservation",
        }],
    }
    path = tmp_path / f"{name}-owned.json"
    path.write_text(json.dumps(value), encoding="utf-8")
    return path


def setup_case(
    tmp_path: Path,
    *,
    exit_code: int = 0,
    candidate_exists: bool = True,
    selected_role: str = "candidate",
):
    source = tmp_path / "source.mp4"
    snapshot = tmp_path / "snapshot.json"
    ffmpeg = tmp_path / "ffmpeg.exe"
    helper = tmp_path / "helper.exe"
    baseline_worker = tmp_path / "baseline-worker.exe"
    candidate_worker = tmp_path / "candidate-worker.exe"
    for path, contents in [
        (source, b"source"),
        (snapshot, b"snapshot"),
        (ffmpeg, b"ffmpeg baseline"),
        (helper, b"helper baseline"),
        (baseline_worker, b"worker baseline"),
    ]:
        path.write_bytes(contents)
    if candidate_exists:
        candidate_worker.write_bytes(b"worker candidate")
    raw_manifest = owned_manifest(tmp_path, "raw", b"existing raw")
    build_manifest = owned_manifest(tmp_path, "build", b"existing build")

    fake_go = tmp_path / "fake-go.ps1"
    fake_go.write_text(
        "if ($env:FAKE_SLEEP -eq '1') { $child=Start-Process -FilePath $env:FAKE_PWSH -ArgumentList @('-NoProfile','-Command','Start-Sleep -Seconds 30') -PassThru; @{parent=$PID;child=$child.Id} | ConvertTo-Json -Compress | Set-Content -LiteralPath $env:FAKE_PIDS; Start-Sleep -Seconds 30 }; if ($env:RACE_MANIFEST -eq '1') { Set-Content -LiteralPath $env:RACE_MANIFEST_PATH -Value '{\"race\":true}' -NoNewline }; @{worker=$env:NICO_TIMELINE_WORKER_EXE;width=$env:NICO_TIMELINE_WORKER_WIDTH;height=$env:NICO_TIMELINE_WORKER_HEIGHT;duration_ms=$env:NICO_TIMELINE_WORKER_DURATION_MS;fps_num=$env:NICO_TIMELINE_WORKER_FPS_NUM;fps_den=$env:NICO_TIMELINE_WORKER_FPS_DEN;encoder=$env:NICO_TIMELINE_WORKER_ENCODER;output_mode=$env:NICO_TIMELINE_WORKER_OUTPUT_MODE;gpu_backend=$env:NICO_TIMELINE_WORKER_GPU_BACKEND;cpu_limit=$env:NICO_TIMELINE_WORKER_TEST_CPU_PERCENT;working_directory=$pwd.Path;go_arguments=$args} | ConvertTo-Json -Compress | Set-Content -LiteralPath $env:FAKE_CAPTURE; exit $env:FAKE_EXIT",
        encoding="utf-8",
    )
    capture = tmp_path / "selected-worker.txt"
    run_root = tmp_path / "run-root"
    run_manifest = tmp_path / "run-manifest.json"
    args = [
        POWERSHELL,
        "-NoProfile",
        "-File",
        str(SCRIPT),
        "-RunColdCase",
        "-RunSourceVideo",
        str(source),
        "-RunSnapshot",
        str(snapshot),
        "-RunFfmpeg",
        str(ffmpeg),
        "-BaselineWorker",
        str(baseline_worker),
        "-CandidateWorker",
        str(candidate_worker),
        "-RunHelper",
        str(helper),
        "-SelectedWorker",
        selected_role,
        "-SessionMode",
        "cold",
        "-StreamProtocol",
        "NCT1",
        "-RunRoot",
        str(run_root),
        "-RunManifestPath",
        str(run_manifest),
        "-BaselineId",
        "baseline-fixture",
        "-CandidateId",
        "candidate-fixture",
        "-PairId",
        "pair-shared-0001",
        "-RawOwnedManifests",
        str(raw_manifest),
        "-BuildOwnedManifests",
        str(build_manifest),
        "-Width",
        "1920",
        "-Height",
        "1080",
        "-DurationMs",
        "6000",
        "-FpsNum",
        "30",
        "-FpsDen",
        "1",
        "-Encoder",
        "nvenc",
        "-OutputMode",
        "tee",
        "-GpuBackend",
        "vulkan",
        "-CpuLimit",
        "0",
        "-TimeoutSeconds",
        "60",
        "-GoExecutable",
        POWERSHELL,
        "-GoArgumentsJson",
        json.dumps(["-NoProfile", "-File", str(fake_go)]),
    ]
    return args, {
        "selected_role": selected_role,
        "baseline": baseline_worker,
        "candidate": candidate_worker,
        "capture": capture,
        "pids": tmp_path / "fake-pids.json",
        "run_root": run_root,
        "run_manifest": run_manifest,
        "fake_go": fake_go,
        "exit_code": exit_code,
    }


def run_case(tmp_path: Path, **kwargs):
    args, fixture = setup_case(tmp_path, **kwargs)
    env = dict(os.environ)
    env["FAKE_CAPTURE"] = str(fixture["capture"])
    env["FAKE_EXIT"] = str(fixture["exit_code"])
    env["NICO_TIMELINE_WORKER_WIDTH"] = "640"
    env["NICO_TIMELINE_WORKER_ENCODER"] = "inherited-value"
    result = subprocess.run(args, capture_output=True, text=True, env=env, check=False, timeout=30)
    return result, fixture


def test_cold_case_executes_selected_candidate_and_records_identity(tmp_path):
    result, fixture = run_case(tmp_path)
    assert result.returncode == 0, result.stderr
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    child = json.loads(fixture["capture"].read_text(encoding="utf-8"))
    assert Path(child["worker"]) == fixture["candidate"]
    assert run["run_status"] == "completed"
    assert run["sample_eligible"] is True
    assert run["plan_version"] == 4
    assert run["executable"]["selected_role"] == "candidate"
    assert run["executable"]["path"] == str(fixture["candidate"])
    assert run["executable"]["sha256"] == sha(fixture["candidate"])
    assert run["executable"]["size_bytes"] == fixture["candidate"].stat().st_size
    owned = json.loads(Path(run["owned_artifact_manifest"]).read_text(encoding="utf-8"))
    assert owned["root"] == str(fixture["run_root"])
    assert {Path(row["path"]).name for row in owned["artifacts"]} == {"child.log"}
    assert run["owned_artifact_bytes"] == sum(row["size_bytes"] for row in owned["artifacts"])
    assert run["pair_id"] == "pair-shared-0001"
    assert run["inputs"]["source_video"]["sha256"] == sha(tmp_path / "source.mp4")
    assert run["inputs"]["snapshot"]["sha256"] == sha(tmp_path / "snapshot.json")
    assert run["inputs"]["ffmpeg"]["sha256"] == sha(tmp_path / "ffmpeg.exe")
    assert run["inputs"]["helper"]["sha256"] == sha(tmp_path / "helper.exe")
    for input_name, path in {
        "source_video": tmp_path / "source.mp4",
        "snapshot": tmp_path / "snapshot.json",
        "ffmpeg": tmp_path / "ffmpeg.exe",
        "helper": tmp_path / "helper.exe",
    }.items():
        assert run["inputs"][input_name]["size_bytes"] == path.stat().st_size
    evidence = run["capacity_evidence"]
    assert {item["category"] for item in evidence["manifests"]} == {"raw", "build"}
    assert all(item["sha256"] == sha(Path(item["path"])) for item in evidence["manifests"])
    assert evidence["raw"]["total_bytes"] == len(b"existing raw")
    assert evidence["build"]["total_bytes"] == len(b"existing build")
    assert evidence["raw"]["volume_id"]
    assert evidence["raw"]["free_bytes"] >= 20 * 1024**3
    conditions = {
        "width": 1920,
        "height": 1080,
        "duration_ms": 6000,
        "fps_num": 30,
        "fps_den": 1,
        "encoder": "nvenc",
        "output_mode": "tee",
        "gpu_backend": "vulkan",
        "cpu_limit": 0,
    }
    assert run["worker_conditions"] == conditions
    assert run["working_directory"] == str(SCRIPT.parents[3])
    assert run["timeout_seconds"] == 60
    assert run["child_command"]["executable"] == POWERSHELL
    assert run["child_command"]["arguments"] == ["-NoProfile", "-File", str(fixture["fake_go"])]
    assert run["child_command"]["working_directory"] == str(SCRIPT.parents[3])
    assert {key: child[key] for key in conditions} == {
        **{key: str(value) for key, value in conditions.items() if isinstance(value, int)},
        **{key: value for key, value in conditions.items() if isinstance(value, str)},
    }


def test_capacity_evidence_uses_one_free_space_snapshot_per_volume(tmp_path):
    raw_one = owned_manifest(tmp_path, "raw-one", b"raw one")
    raw_two = owned_manifest(tmp_path, "raw-two", b"raw two")
    build_one = owned_manifest(tmp_path, "build-one", b"build one")
    build_two = owned_manifest(tmp_path, "build-two", b"build two")
    harness = tmp_path / "capacity-evidence-fixture.ps1"
    harness.write_text(
        "\n".join([
            f"Import-Module {ps_quote(Path(__file__).with_name('next-speed-artifacts.psm1'))} -Force",
            "function Resolve-RegularFile([string]$Path,[string]$Label) { $resolved=Resolve-Path -LiteralPath $Path -ErrorAction Stop; $item=Get-Item -LiteralPath $resolved.ProviderPath -Force; if ($item -isnot [System.IO.FileInfo]) { throw \"$Label must be a file\" }; return $item }",
            "function Get-Sha256([string]$Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant() }",
            "$tokens=$null; $parseErrors=$null; $ast=[System.Management.Automation.Language.Parser]::ParseFile(" + ps_quote(SCRIPT) + ", [ref]$tokens, [ref]$parseErrors)",
            "$functionAst=$ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-OwnedCapacityEvidence' }, $true)",
            "if ($parseErrors.Count -gt 0 -or $null -eq $functionAst) { throw 'Could not load capacity evidence function.' }",
            ". ([scriptblock]::Create($functionAst.Extent.Text))",
            "$script:sampleCalls=0",
            "$sampleProvider={ param($volumeId) $script:sampleCalls++; if ($script:sampleCalls -eq 1) { return [long](50GB) }; return [long](1GB) }",
            f"$evidence=Get-OwnedCapacityEvidence -RawPaths @({ps_quote(raw_one)},{ps_quote(raw_two)}) -BuildPaths @({ps_quote(build_one)},{ps_quote(build_two)}) -FreeSpaceSnapshotProvider $sampleProvider",
            "[ordered]@{ evidence=$evidence; sample_calls=$script:sampleCalls } | ConvertTo-Json -Compress -Depth 8",
        ]),
        encoding="utf-8",
    )

    result = subprocess.run(
        [POWERSHELL, "-NoProfile", "-File", str(harness)],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )

    assert result.returncode == 0, result.stderr
    response = json.loads(result.stdout.splitlines()[-1])
    evidence = response["evidence"]
    assert response["sample_calls"] == 1
    assert evidence["raw"]["free_bytes"] == 50 * 1024**3
    assert evidence["build"]["free_bytes"] == 50 * 1024**3
    assert len(evidence["volume_snapshots"]) == 1
    assert {item["free_bytes"] for item in evidence["manifests"]} == {50 * 1024**3}
    assert {item["free_space_sampled_utc"] for item in evidence["manifests"]} == {
        evidence["volume_snapshots"][0]["sampled_utc"]
    }


def test_successful_cold_run_manifest_is_accepted_as_eligible(tmp_path):
    result, fixture = run_case(tmp_path)
    assert result.returncode == 0, result.stderr
    validation = subprocess.run(
        [
            POWERSHELL,
            "-NoProfile",
            "-File",
            str(SCRIPT),
            "-RunManifest",
            str(fixture["run_manifest"]),
            "-RequestedSessionMode",
            "cold",
            "-RequestedStreamProtocol",
            "NCT1",
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert validation.returncode == 0, validation.stderr
    assert "run_status=completed" in validation.stdout
    assert "sample_eligible=true" in validation.stdout


def test_manifest_rename_race_cleans_temp_and_never_creates_eligible_sample(tmp_path):
    args, fixture = setup_case(tmp_path)
    env = dict(os.environ)
    env["FAKE_CAPTURE"] = str(fixture["capture"])
    env["FAKE_EXIT"] = "0"
    env["RACE_MANIFEST"] = "1"
    env["RACE_MANIFEST_PATH"] = str(fixture["run_manifest"])
    result = subprocess.run(args, capture_output=True, text=True, env=env, check=False, timeout=30)
    assert result.returncode != 0
    assert fixture["run_manifest"].exists()
    assert list(tmp_path.glob("run-manifest.json.*.tmp")) == []

    validation = subprocess.run(
        [
            POWERSHELL,
            "-NoProfile",
            "-File",
            str(SCRIPT),
            "-RunManifest",
            str(fixture["run_manifest"]),
            "-RequestedSessionMode",
            "cold",
            "-RequestedStreamProtocol",
            "NCT1",
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert validation.returncode != 0
    assert "sample_eligible=true" not in validation.stdout


def test_baseline_selection_swaps_the_executable_passed_to_child(tmp_path):
    args, fixture = setup_case(tmp_path, selected_role="baseline")
    env = dict(os.environ)
    env["FAKE_CAPTURE"] = str(fixture["capture"])
    env["FAKE_EXIT"] = "0"
    result = subprocess.run(args, capture_output=True, text=True, env=env, check=False, timeout=30)
    assert result.returncode == 0, result.stderr
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    child = json.loads(fixture["capture"].read_text(encoding="utf-8"))
    assert Path(child["worker"]) == fixture["baseline"]
    assert run["executable"]["selected_role"] == "baseline"
    assert run["executable"]["path"] == str(fixture["baseline"])
    assert run["executable"]["sha256"] == sha(fixture["baseline"])


def test_two_cases_reuse_the_caller_pair_id(tmp_path):
    observed = []
    for name in ("member-a", "member-b"):
        case_dir = tmp_path / name
        case_dir.mkdir()
        args, fixture = setup_case(case_dir)
        env = dict(os.environ)
        env["FAKE_CAPTURE"] = str(fixture["capture"])
        env["FAKE_EXIT"] = "0"
        result = subprocess.run(args, capture_output=True, text=True, env=env, check=False, timeout=30)
        assert result.returncode == 0, result.stderr
        run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
        observed.append(run["pair_id"])
    assert observed == ["pair-shared-0001", "pair-shared-0001"]


def test_missing_owned_manifest_is_rejected_before_child_start(tmp_path):
    args, fixture = setup_case(tmp_path)
    index = args.index("-BuildOwnedManifests")
    del args[index : index + 2]
    result = subprocess.run(
        args,
        capture_output=True,
        text=True,
        env={**os.environ, "FAKE_CAPTURE": str(fixture["capture"]), "FAKE_EXIT": "0"},
        check=False,
        timeout=30,
    )
    assert result.returncode != 0
    assert not fixture["capture"].exists()


def test_invalid_pair_id_is_failed_and_ineligible(tmp_path):
    args, fixture = setup_case(tmp_path)
    args[args.index("pair-shared-0001")] = "pair id with spaces"
    result = subprocess.run(
        args,
        capture_output=True,
        text=True,
        env={**os.environ, "FAKE_CAPTURE": str(fixture["capture"]), "FAKE_EXIT": "0"},
        check=False,
        timeout=30,
    )
    assert result.returncode != 0
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "failed"
    assert run["sample_eligible"] is False
    assert "PairId has invalid format" in run["failure_reason"]
    assert not fixture["capture"].exists()


def test_child_timeout_kills_and_waits_for_the_entire_process_tree(tmp_path):
    args, fixture = setup_case(tmp_path)
    args[args.index("60", args.index("-TimeoutSeconds") + 1)] = "1"
    env = {
        **os.environ,
        "FAKE_CAPTURE": str(fixture["capture"]),
        "FAKE_PIDS": str(fixture["pids"]),
        "FAKE_PWSH": POWERSHELL,
        "FAKE_SLEEP": "1",
        "FAKE_EXIT": "0",
    }
    result = subprocess.run(args, capture_output=True, text=True, env=env, check=False, timeout=30)
    assert result.returncode != 0
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "failed"
    assert run["sample_eligible"] is False
    assert "timed out" in run["failure_reason"]
    pids = json.loads(fixture["pids"].read_text(encoding="utf-8"))
    for pid in (pids["parent"], pids["child"]):
        deadline = time.monotonic() + 3
        while process_is_running(pid) and time.monotonic() < deadline:
            time.sleep(0.05)
        assert not process_is_running(pid), f"process {pid} was left running after timeout"


def test_runner_cancel_kills_and_waits_for_the_entire_process_tree(tmp_path):
    args, fixture = setup_case(tmp_path)
    env = {
        **os.environ,
        "FAKE_CAPTURE": str(fixture["capture"]),
        "FAKE_PIDS": str(fixture["pids"]),
        "FAKE_PWSH": POWERSHELL,
        "FAKE_SLEEP": "1",
        "FAKE_EXIT": "0",
    }
    runner = subprocess.Popen(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        creationflags=subprocess.CREATE_NEW_PROCESS_GROUP,
    )
    try:
        deadline = time.monotonic() + 10
        while not fixture["pids"].exists() and runner.poll() is None and time.monotonic() < deadline:
            time.sleep(0.05)
        assert fixture["pids"].exists(), "fake process tree did not start"
        runner.send_signal(signal.CTRL_BREAK_EVENT)
        stdout, stderr = runner.communicate(timeout=20)
        assert runner.returncode != 0, stdout
        run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
        assert run["run_status"] == "failed"
        assert run["sample_eligible"] is False
        assert "cancelled" in run["failure_reason"]
        pids = json.loads(fixture["pids"].read_text(encoding="utf-8"))
        for pid in (pids["parent"], pids["child"]):
            deadline = time.monotonic() + 3
            while process_is_running(pid) and time.monotonic() < deadline:
                time.sleep(0.05)
            assert not process_is_running(pid), f"process {pid} was left running after cancellation"
    finally:
        if runner.poll() is None:
            runner.kill()
            runner.wait(timeout=5)
        if fixture["pids"].exists():
            pids = json.loads(fixture["pids"].read_text(encoding="utf-8"))
            for pid in (pids["parent"], pids["child"]):
                terminate_process(pid)


def process_is_running(pid: int) -> bool:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [ctypes.c_uint32, ctypes.c_int, ctypes.c_uint32]
    kernel32.OpenProcess.restype = ctypes.c_void_p
    kernel32.WaitForSingleObject.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
    kernel32.WaitForSingleObject.restype = ctypes.c_uint32
    kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
    handle = kernel32.OpenProcess(0x00100000, False, pid)
    if not handle:
        return False
    try:
        return kernel32.WaitForSingleObject(handle, 0) == 0x00000102
    finally:
        kernel32.CloseHandle(handle)


def terminate_process(pid: int) -> None:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [ctypes.c_uint32, ctypes.c_int, ctypes.c_uint32]
    kernel32.OpenProcess.restype = ctypes.c_void_p
    kernel32.TerminateProcess.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
    kernel32.TerminateProcess.restype = ctypes.c_int
    kernel32.WaitForSingleObject.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
    kernel32.WaitForSingleObject.restype = ctypes.c_uint32
    kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
    handle = kernel32.OpenProcess(0x0001 | 0x00100000, False, pid)
    if not handle:
        return
    try:
        kernel32.TerminateProcess(handle, 1)
        kernel32.WaitForSingleObject(handle, 5000)
    finally:
        kernel32.CloseHandle(handle)


def test_run_manifest_inside_run_root_is_rejected_before_child_start(tmp_path):
    args, fixture = setup_case(tmp_path)
    args[args.index("-RunManifestPath") + 1] = str(fixture["run_root"] / "run-manifest.json")
    result = subprocess.run(
        args,
        capture_output=True,
        text=True,
        env={**os.environ, "FAKE_CAPTURE": str(fixture["capture"]), "FAKE_EXIT": "0"},
        check=False,
        timeout=30,
    )
    assert result.returncode != 0
    assert "RunManifestPath must be outside RunRoot" in result.stderr
    assert not fixture["capture"].exists()


def test_invalid_owned_manifest_is_failed_and_ineligible(tmp_path):
    args, fixture = setup_case(tmp_path)
    path = Path(args[args.index("-BuildOwnedManifests") + 1])
    value = json.loads(path.read_text(encoding="utf-8"))
    value["artifacts"][0]["sha256"] = "0" * 64
    path.write_text(json.dumps(value), encoding="utf-8")
    result = subprocess.run(
        args,
        capture_output=True,
        text=True,
        env={**os.environ, "FAKE_CAPTURE": str(fixture["capture"]), "FAKE_EXIT": "0"},
        check=False,
        timeout=30,
    )
    assert result.returncode != 0
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "failed"
    assert run["sample_eligible"] is False
    assert not fixture["capture"].exists()


def test_nonzero_child_exit_is_failed_and_ineligible(tmp_path):
    result, fixture = run_case(tmp_path, exit_code=23)
    assert result.returncode != 0
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "failed"
    assert run["sample_eligible"] is False
    assert run["child_exit_code"] == 23


def test_missing_candidate_is_recorded_ineligible_without_launch(tmp_path):
    result, fixture = run_case(tmp_path, candidate_exists=False)
    assert result.returncode != 0
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "failed"
    assert run["sample_eligible"] is False
    assert run["executable"]["sha256"] is None
    assert not fixture["capture"].exists()


def test_unsupported_mode_is_explicit_skip_and_never_launches(tmp_path):
    args, fixture = setup_case(tmp_path)
    args[args.index("NCT1")] = "NCT2"
    result = subprocess.run(args, capture_output=True, text=True, check=False, timeout=30)
    assert result.returncode == 0, result.stderr
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "skipped"
    assert run["skip_reason"]
    assert run["sample_eligible"] is False
    assert not fixture["capture"].exists()


def test_tampered_capacity_size_is_rejected_and_child_is_never_started(tmp_path):
    args, fixture = setup_case(tmp_path)
    raw_manifest = Path(args[args.index("-RawOwnedManifests") + 1])
    value = json.loads(raw_manifest.read_text(encoding="utf-8"))
    value["artifacts"][0]["size_bytes"] = 13 * 1024**3
    raw_manifest.write_text(json.dumps(value), encoding="utf-8")
    env = dict(os.environ)
    env["FAKE_CAPTURE"] = str(fixture["capture"])
    env["FAKE_EXIT"] = "0"
    result = subprocess.run(args, capture_output=True, text=True, env=env, check=False, timeout=30)
    assert result.returncode != 0
    run = json.loads(fixture["run_manifest"].read_text(encoding="utf-8"))
    assert run["run_status"] == "failed"
    assert run["sample_eligible"] is False
    assert "Manifest artifact size/hash mismatch" in run["failure_reason"]
    assert not fixture["capture"].exists()
