from __future__ import annotations

import hashlib
import importlib.util
import json
import struct
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "verify-nico-timeline-payload.py"
SPEC = importlib.util.spec_from_file_location("verify_nico_timeline_payload", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
VERIFIER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFIER)


def _pe_amd64() -> bytes:
    header = bytearray(70)
    header[:2] = b"MZ"
    struct.pack_into("<I", header, 0x3C, 64)
    header[64:68] = b"PE\0\0"
    struct.pack_into("<H", header, 68, 0x8664)
    return bytes(header)


def _manifest(binary: bytes) -> dict[str, object]:
    return {
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
        "os": "windows",
        "architecture": "amd64",
        "sha256": hashlib.sha256(binary).hexdigest(),
        "wgpuVersion": "0.20.1",
        "cargoTarget": "x86_64-pc-windows-msvc",
    }


class VerifyNicoTimelinePayloadTests(unittest.TestCase):
    def _write_payload(self, directory: Path, binary: bytes, manifest: dict[str, object]) -> None:
        (directory / "nico-compositord.bin").write_bytes(binary)
        (directory / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")

    def test_accepts_hash_valid_targeted_nct2_pe(self) -> None:
        binary = _pe_amd64()
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self._write_payload(directory, binary, _manifest(binary))
            VERIFIER.verify(directory, "windows", "amd64")

    def test_rejects_legacy_nct1_manifest(self) -> None:
        binary = _pe_amd64()
        manifest = _manifest(binary)
        manifest["protocol"] = "NCT1"
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self._write_payload(directory, binary, manifest)
            with self.assertRaisesRegex(ValueError, "NCT2"):
                VERIFIER.verify(directory, "windows", "amd64")

    def test_rejects_hash_target_and_machine_mismatch(self) -> None:
        binary = _pe_amd64()
        cases = (
            (lambda manifest: manifest.update(sha256="0" * 64), "SHA-256"),
            (lambda manifest: manifest.update(architecture="arm64"), "target"),
            (lambda manifest: manifest.update(cargoTarget="aarch64-pc-windows-msvc"), "Cargo target"),
        )
        for mutate, message in cases:
            with self.subTest(message=message), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                manifest = _manifest(binary)
                mutate(manifest)
                self._write_payload(directory, binary, manifest)
                with self.assertRaisesRegex(ValueError, message):
                    VERIFIER.verify(directory, "windows", "amd64")

        wrong_machine = bytearray(binary)
        struct.pack_into("<H", wrong_machine, 68, 0xAA64)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            wrong_binary = bytes(wrong_machine)
            self._write_payload(directory, wrong_binary, _manifest(wrong_binary))
            with self.assertRaisesRegex(ValueError, "PE machine"):
                VERIFIER.verify(directory, "windows", "amd64")


if __name__ == "__main__":
    unittest.main()
