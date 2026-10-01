#!/usr/bin/env python3
"""Verify a bundled NCT2 timeline compositor matches a Go release target."""

from __future__ import annotations

import hashlib
import json
import struct
import sys
from pathlib import Path


TARGETS = {
    "windows/amd64": {"x86_64-pc-windows-msvc", "x86_64-pc-windows-gnu"},
    "windows/arm64": {"aarch64-pc-windows-msvc"},
    "darwin/amd64": {"x86_64-apple-darwin"},
    "darwin/arm64": {"aarch64-apple-darwin"},
    "linux/amd64": {"x86_64-unknown-linux-gnu", "x86_64-unknown-linux-musl"},
    "linux/arm64": {"aarch64-unknown-linux-gnu", "aarch64-unknown-linux-musl"},
}
REQUIRED_CAPABILITIES = {
    "incremental-assets",
    "ordered-elements",
    "watermarks",
    "end-and-eof",
}
MANIFEST_FIELDS = {
    "schema",
    "protocol",
    "protocolVersion",
    "inputMode",
    "outputFormat",
    "capabilities",
    "os",
    "architecture",
    "sha256",
    "wgpuVersion",
    "cargoTarget",
}


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate manifest field {key!r}")
        result[key] = value
    return result


def _verify_binary(binary: bytes, goos: str, goarch: str) -> None:
    if goos == "windows":
        if len(binary) < 64 or binary[:2] != b"MZ":
            raise ValueError("timeline helper is not a PE executable")
        pe_offset = struct.unpack_from("<I", binary, 0x3C)[0]
        if pe_offset + 6 > len(binary) or binary[pe_offset : pe_offset + 4] != b"PE\0\0":
            raise ValueError("timeline helper has an invalid PE header")
        machine = struct.unpack_from("<H", binary, pe_offset + 4)[0]
        expected = {"amd64": 0x8664, "arm64": 0xAA64}.get(goarch)
        if expected is None or machine != expected:
            raise ValueError(f"timeline helper PE machine {machine:#x} does not match {goarch}")
        return

    if goos == "linux":
        if len(binary) < 20 or binary[:4] != b"\x7fELF":
            raise ValueError("timeline helper is not an ELF executable")
        byte_order = {1: "<", 2: ">"}.get(binary[5])
        if byte_order is None:
            raise ValueError("timeline helper ELF byte order is invalid")
        machine = struct.unpack_from(byte_order + "H", binary, 18)[0]
        expected = {"amd64": 62, "arm64": 183}.get(goarch)
        if expected is None or machine != expected:
            raise ValueError(f"timeline helper ELF machine {machine} does not match {goarch}")
        return

    if goos == "darwin":
        if len(binary) < 8:
            raise ValueError("timeline helper Mach-O executable is truncated")
        magic = binary[:4]
        byte_order = {
            b"\xcf\xfa\xed\xfe": "<",
            b"\xfe\xed\xfa\xcf": ">",
        }.get(magic)
        if byte_order is None:
            raise ValueError("timeline helper is not a 64-bit Mach-O executable")
        cpu = struct.unpack_from(byte_order + "I", binary, 4)[0]
        expected = {"amd64": 0x01000007, "arm64": 0x0100000C}.get(goarch)
        if expected is None or cpu != expected:
            raise ValueError(f"timeline helper Mach-O CPU {cpu:#x} does not match {goarch}")
        return

    raise ValueError(f"unsupported timeline helper OS {goos!r}")


def verify(directory: Path, goos: str, goarch: str) -> None:
    binary = (directory / "nico-compositord.bin").read_bytes()
    manifest_bytes = (directory / "manifest.json").read_bytes()
    if not binary:
        raise ValueError("timeline helper is empty")
    if not manifest_bytes or len(manifest_bytes) > 16 * 1024:
        raise ValueError("timeline helper manifest size is outside 1..16384 bytes")
    manifest = json.loads(manifest_bytes, object_pairs_hook=_unique_object)
    if not isinstance(manifest, dict) or set(manifest) != MANIFEST_FIELDS:
        raise ValueError("timeline helper manifest fields do not match strict NCT2 schema")
    if type(manifest["schema"]) is not int or manifest["schema"] != 1 or manifest["protocol"] != "NCT2" or type(manifest["protocolVersion"]) is not int or manifest["protocolVersion"] != 2:
        raise ValueError("timeline helper must implement NCT2 protocol version 2")
    if manifest["inputMode"] != "--stdin-stream" or manifest["outputFormat"] != "rgba8":
        raise ValueError("timeline helper has unsupported NCT2 input or output format")
    capabilities = manifest["capabilities"]
    if not isinstance(capabilities, list) or any(not isinstance(item, str) for item in capabilities):
        raise ValueError("timeline helper capabilities must be an array of strings")
    if not REQUIRED_CAPABILITIES.issubset(capabilities):
        raise ValueError("timeline helper is missing required NCT2 capabilities")
    expected_target = f"{goos}/{goarch}"
    if expected_target not in TARGETS:
        raise ValueError(f"unsupported Go target {expected_target}")
    if manifest["os"] != goos or manifest["architecture"] != goarch:
        raise ValueError(f"timeline helper target does not match {expected_target}")
    if manifest["cargoTarget"] not in TARGETS[expected_target]:
        raise ValueError(f"timeline helper Cargo target does not match {expected_target}")
    if manifest["wgpuVersion"] != "0.20.1":
        raise ValueError("timeline helper WGPU version does not match the locked release")
    digest = manifest["sha256"]
    if not isinstance(digest, str) or digest.lower() != hashlib.sha256(binary).hexdigest():
        raise ValueError("timeline helper SHA-256 does not match its manifest")
    _verify_binary(binary, goos, goarch)


def main(argv: list[str]) -> int:
    if len(argv) != 4:
        print("usage: verify-nico-timeline-payload.py PAYLOAD_DIR GOOS GOARCH", file=sys.stderr)
        return 2
    try:
        verify(Path(argv[1]), argv[2], argv[3])
    except (OSError, ValueError, json.JSONDecodeError, struct.error) as exc:
        print(f"Nico timeline payload rejected: {exc}", file=sys.stderr)
        return 1
    print(f"verified NCT2 timeline payload for {argv[2]}/{argv[3]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
