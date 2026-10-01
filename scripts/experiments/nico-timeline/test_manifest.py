import hashlib
import importlib.util
import json
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


SCRIPT = Path(__file__).resolve().parents[2] / "nico_timeline_manifest.py"
SPEC = importlib.util.spec_from_file_location("nico_timeline_manifest", SCRIPT)
MANIFEST = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MANIFEST)


@pytest.mark.parametrize(
    ("target", "os_name", "architecture"),
    [
        ("x86_64-pc-windows-msvc", "windows", "amd64"),
        ("aarch64-apple-darwin", "darwin", "arm64"),
        ("x86_64-unknown-linux-gnu", "linux", "amd64"),
    ],
)
def test_manifest_pins_protocol_platform_version_and_hash(
    target, os_name, architecture
):
    binary = b"deterministic compositor payload"
    manifest = MANIFEST.create_manifest(binary, target)
    assert manifest == {
        "schema": 1,
        "protocol": "NCT1",
        "os": os_name,
        "architecture": architecture,
        "sha256": hashlib.sha256(binary).hexdigest(),
        "wgpuVersion": "0.20.1",
        "cargoTarget": target,
    }


def test_manifest_rejects_empty_payload_and_unknown_target():
    with pytest.raises(ValueError, match="empty"):
        MANIFEST.create_manifest(b"", "x86_64-pc-windows-msvc")
    with pytest.raises(ValueError, match="unsupported Rust target"):
        MANIFEST.create_manifest(b"binary", "wasm32-unknown-unknown")


def test_manifest_write_uses_atomic_replace(tmp_path):
    binary = tmp_path / "helper"
    binary.write_bytes(b"compositor")
    manifest = tmp_path / "out" / "manifest.json"
    calls = []

    def legacy_runner(path, args, *, timeout_seconds, output_limit_bytes):
        calls.append((path, args, timeout_seconds, output_limit_bytes))
        if args[0] == "--capabilities":
            return SimpleNamespace(
                returncode=2,
                stdout=b"",
                stderr=b'NICO_ERROR unknown argument "--capabilities"\n',
            )
        return SimpleNamespace(
            returncode=0,
            stdout=valid_nct1_self_test(),
            stderr=b"",
        )

    MANIFEST.write_manifest(
        binary, "x86_64-pc-windows-msvc", manifest, runner=legacy_runner
    )
    assert '"protocol": "NCT1"' in manifest.read_text(encoding="utf-8")
    assert not Path(str(manifest) + ".tmp").exists()
    assert [call[1][0] for call in calls] == ["--capabilities", "--self-test"]


def test_older_nct1_self_test_without_asset_layout_fields_stays_compatible(tmp_path):
    binary = tmp_path / "helper"
    binary.write_bytes(b"older legacy compositor")
    output = tmp_path / "manifest.json"
    report = json.loads(valid_nct1_self_test())
    for field in (
        "requestedAssetLayout",
        "assetLayout",
        "assetPageCount",
        "assetSourceBytes",
        "assetAllocatedBytes",
    ):
        del report[field]

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        if args[0] == "--capabilities":
            return SimpleNamespace(
                returncode=2,
                stdout=b"",
                stderr=b'NICO_ERROR unknown argument "--capabilities"',
            )
        return SimpleNamespace(
            returncode=0, stdout=json.dumps(report).encode(), stderr=b""
        )

    MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    manifest = json.loads(output.read_text(encoding="utf-8"))
    assert manifest["protocol"] == "NCT1"
    assert "protocolVersion" not in manifest
    assert "capabilities" not in manifest


def valid_nct1_self_test():
    return json.dumps(
        {
            "schema": 1,
            "protocol": "NCT1",
            "renderer": "wgpu",
            "version": "0.1.0",
            "backend": "vulkan",
            "adapterName": "GPU",
            "adapterType": "DiscreteGpu",
            "readbackSlots": 3,
            "requestedAssetLayout": "separate",
            "assetLayout": "separate",
            "assetPageCount": 0,
            "assetSourceBytes": 0,
            "assetAllocatedBytes": 0,
            "maxTextureDimension2D": 16384,
            "testFrameCount": 3,
            "testFrameBytes": 7524,
            "testFrameSHA256": "a" * 64,
        }
    ).encode("utf-8")


def valid_nct2_capabilities():
    return json.dumps(
        {
            "schema": 1,
            "protocol": "NCT2",
            "protocolVersion": 2,
            "inputMode": "--stdin-stream",
            "outputFormat": "rgba8",
            "capabilities": [
                "incremental-assets",
                "ordered-elements",
                "watermarks",
                "end-and-eof",
            ],
        }
    ).encode("utf-8")


def test_nct2_manifest_is_bound_to_inspected_binary_and_declared_capabilities(tmp_path):
    binary = tmp_path / "helper"
    payload = b"same exact NCT2 compositor bytes"
    binary.write_bytes(payload)
    output = tmp_path / "manifest.json"
    calls = []

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        calls.append((path.resolve(), args, timeout_seconds, output_limit_bytes))
        assert path.resolve() == binary.resolve()
        assert args == ["--capabilities", "--protocol-version", "2"]
        return SimpleNamespace(
            returncode=0,
            stdout=valid_nct2_capabilities(),
            stderr=b"",
        )

    MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    manifest = json.loads(output.read_text(encoding="utf-8"))
    assert manifest["protocol"] == "NCT2"
    assert manifest["protocolVersion"] == 2
    assert manifest["inputMode"] == "--stdin-stream"
    assert manifest["outputFormat"] == "rgba8"
    assert (
        manifest["capabilities"]
        == json.loads(valid_nct2_capabilities())["capabilities"]
    )
    assert manifest["sha256"] == hashlib.sha256(payload).hexdigest()
    assert calls == [
        (binary.resolve(), ["--capabilities", "--protocol-version", "2"], 10.0, 65536)
    ]


@pytest.mark.parametrize(
    "missing_field",
    [
        "schema",
        "protocol",
        "protocolVersion",
        "inputMode",
        "outputFormat",
        "capabilities",
    ],
)
def test_nct2_manifest_rejects_each_missing_capability_field(tmp_path, missing_field):
    binary = tmp_path / "helper"
    binary.write_bytes(b"helper")
    output = tmp_path / "manifest.json"
    response = json.loads(valid_nct2_capabilities())
    del response[missing_field]

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        return SimpleNamespace(
            returncode=0, stdout=json.dumps(response).encode(), stderr=b""
        )

    with pytest.raises(ValueError):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert not output.exists()
    assert not Path(str(output) + ".tmp").exists()


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("protocolVersion", 1),
        ("protocol", "NCT1"),
        ("inputMode", "--stdin"),
        ("outputFormat", "rgb8"),
        ("capabilities", ["incremental-assets", "ordered-elements", "watermarks"]),
        ("futureField", True),
    ],
    ids=[
        "wrong-version",
        "wrong-protocol",
        "wrong-input",
        "wrong-output",
        "missing-capability",
        "unknown-field",
    ],
)
def test_invalid_nct2_inspection_never_writes_a_manifest(tmp_path, field, value):
    binary = tmp_path / "helper"
    binary.write_bytes(b"helper")
    output = tmp_path / "manifest.json"
    response = json.loads(valid_nct2_capabilities())
    response[field] = value

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        return SimpleNamespace(
            returncode=0, stdout=json.dumps(response).encode(), stderr=b""
        )

    with pytest.raises(ValueError):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert not output.exists()
    assert not Path(str(output) + ".tmp").exists()


@pytest.mark.parametrize(
    "response",
    [
        valid_nct2_capabilities() + b" {}",
        valid_nct2_capabilities().replace(b'"schema": 1', b'"schema": 1, "schema": 1'),
        valid_nct2_capabilities() + b" x",
        b"",
        b"not json",
    ],
    ids=[
        "trailing-object",
        "duplicate-field",
        "trailing-token",
        "empty",
        "invalid-json",
    ],
)
def test_malformed_nct2_inspection_is_rejected(tmp_path, response):
    binary = tmp_path / "helper"
    binary.write_bytes(b"helper")
    output = tmp_path / "manifest.json"

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        return SimpleNamespace(returncode=0, stdout=response, stderr=b"")

    with pytest.raises(ValueError):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert not output.exists()


@pytest.mark.parametrize(
    "result",
    [
        SimpleNamespace(returncode=1, stdout=b"", stderr=b"NICO_ERROR GPU unavailable"),
        SimpleNamespace(returncode=None, stdout=b"", stderr=b"", timed_out=True),
        SimpleNamespace(
            returncode=2,
            stdout=b"noise",
            stderr=b'NICO_ERROR unknown argument "--capabilities"',
        ),
        SimpleNamespace(
            returncode=2,
            stdout=b"",
            stderr=b'NICO_ERROR unknown argument "--capabilities"',
            stderr_exceeded=True,
        ),
        SimpleNamespace(
            returncode=2,
            stdout=b"",
            stderr=b'NICO_ERROR unknown argument "--capabilities"',
            stdout_exceeded=True,
        ),
    ],
    ids=[
        "other-error",
        "timeout",
        "unknown-option-with-stdout",
        "stderr-overflow",
        "stdout-overflow",
    ],
)
def test_nonlegacy_probe_failures_do_not_fall_back_to_nct1(tmp_path, result):
    binary = tmp_path / "helper"
    binary.write_bytes(b"helper")
    output = tmp_path / "manifest.json"
    calls = []

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        calls.append(args)
        return result

    with pytest.raises(ValueError):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert calls == [["--capabilities", "--protocol-version", "2"]]
    assert not output.exists()


def test_legacy_fallback_requires_valid_nct1_self_test(tmp_path):
    binary = tmp_path / "helper"
    binary.write_bytes(b"legacy helper")
    output = tmp_path / "manifest.json"
    calls = []

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        calls.append(args)
        if args[0] == "--capabilities":
            return SimpleNamespace(
                returncode=2,
                stdout=b"",
                stderr=b'NICO_ERROR unknown argument "--capabilities"',
            )
        return SimpleNamespace(returncode=0, stdout=b"{} {}", stderr=b"")

    with pytest.raises(ValueError):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert calls == [
        ["--capabilities", "--protocol-version", "2"],
        ["--self-test", "--backend", "auto", "--readback-slots", "3"],
    ]
    assert not output.exists()


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("version", " "),
        ("backend", "gl"),
        ("adapterName", ""),
        ("adapterType", "Cpu"),
        ("readbackSlots", 2),
        ("maxTextureDimension2D", 32),
        ("testFrameCount", 2),
        ("testFrameBytes", 1),
        ("testFrameSHA256", "z" * 64),
        ("backend", []),
        ("adapterName", 7),
        ("adapterType", []),
        ("readbackSlots", True),
        ("maxTextureDimension2D", "16384"),
        ("testFrameCount", False),
        ("testFrameSHA256", "aa " + "a" * 61),
    ],
    ids=[
        "blank-version",
        "unsupported-backend",
        "blank-adapter",
        "software-adapter",
        "wrong-slots",
        "texture-limit",
        "frame-count",
        "frame-bytes",
        "invalid-frame-hash",
        "backend-type",
        "adapter-name-type",
        "adapter-type-type",
        "boolean-slots",
        "texture-limit-type",
        "boolean-frame-count",
        "whitespace-frame-hash",
    ],
)
def test_legacy_fallback_rejects_invalid_nct1_self_test_values(tmp_path, field, value):
    binary = tmp_path / "helper"
    binary.write_bytes(b"legacy helper")
    output = tmp_path / "manifest.json"
    report = json.loads(valid_nct1_self_test())
    report[field] = value
    calls = []

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        calls.append(args)
        if args[0] == "--capabilities":
            return SimpleNamespace(
                returncode=2,
                stdout=b"",
                stderr=b'NICO_ERROR unknown argument "--capabilities"',
            )
        return SimpleNamespace(
            returncode=0, stdout=json.dumps(report).encode(), stderr=b""
        )

    with pytest.raises(ValueError):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert calls == [
        ["--capabilities", "--protocol-version", "2"],
        ["--self-test", "--backend", "auto", "--readback-slots", "3"],
    ]
    assert not output.exists()


def test_binary_modified_during_capability_inspection_is_rejected(tmp_path):
    binary = tmp_path / "helper"
    binary.write_bytes(b"original helper")
    output = tmp_path / "manifest.json"

    def runner(path, args, *, timeout_seconds, output_limit_bytes):
        binary.write_bytes(b"replacement helper")
        return SimpleNamespace(
            returncode=0, stdout=valid_nct2_capabilities(), stderr=b""
        )

    with pytest.raises(ValueError, match="changed while being inspected"):
        MANIFEST.write_manifest(binary, "x86_64-pc-windows-msvc", output, runner=runner)
    assert not output.exists()


def test_default_helper_runner_bounds_response_and_timeout():
    oversized = MANIFEST.run_helper_bounded(
        Path(sys.executable),
        ["-c", "import sys; sys.stdout.write('x' * 100000)"],
        timeout_seconds=5.0,
        output_limit_bytes=1024,
    )
    assert oversized.stdout_exceeded
    assert len(oversized.stdout) <= 1025
    assert not oversized.timed_out

    timed_out = MANIFEST.run_helper_bounded(
        Path(sys.executable),
        ["-c", "import time; time.sleep(2)"],
        timeout_seconds=0.1,
        output_limit_bytes=1024,
    )
    assert timed_out.timed_out
    assert timed_out.returncode != 0
