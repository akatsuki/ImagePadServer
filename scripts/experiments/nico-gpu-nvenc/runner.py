"""Run the isolated probe without downloading, copying, or changing PATH."""

from __future__ import annotations

import argparse
import json
import os
import platform
import shutil
import subprocess
from pathlib import Path
from typing import Any, Callable, Mapping

from manifest import build_result, write_result


EXIT_PASS = 0
EXIT_FAILURE = 1
EXIT_UNAVAILABLE = 2


def _file_exists(path: Path) -> bool:
    return path.is_file()


def _sdk_header_candidates(sdk_root: Path | None) -> list[Path]:
    if sdk_root is None:
        return []
    return [
        sdk_root / "Interface" / "nvEncodeAPI.h",
        sdk_root / "include" / "nvEncodeAPI.h",
        sdk_root / "nvEncodeAPI.h",
    ]


def _runtime_candidates(environment: Mapping[str, str]) -> list[Path]:
    explicit = environment.get("NVENC_RUNTIME_DLL")
    system_root = environment.get("SystemRoot") or environment.get("WINDIR")
    candidates = [Path(explicit)] if explicit else []
    if system_root:
        candidates.append(Path(system_root) / "System32" / "NvEncodeAPI64.dll")
    return candidates


def inspect_environment(
    sdk_root: Path | None,
    *,
    platform_name: str | None = None,
    environment: Mapping[str, str] | None = None,
    command_lookup: Callable[[str], str | None] = shutil.which,
    path_exists: Callable[[Path], bool] = _file_exists,
) -> dict[str, Any]:
    """Inspect only existing local prerequisites; never install or mutate them."""
    env = dict(environment or os.environ)
    system = (platform_name or platform.system()).lower()
    checks: dict[str, bool] = {}
    missing: list[str] = []
    toolchain: dict[str, Any] = {"sdk_root": str(sdk_root) if sdk_root else None}

    if system not in {"windows", "win32", "nt"}:
        missing.append("windows_required")
        checks["windows"] = False
    else:
        checks["windows"] = True
        cl = command_lookup("cl")
        vswhere = command_lookup("vswhere")
        if not vswhere:
            program_files_x86 = env.get("ProgramFiles(x86)")
            if program_files_x86:
                candidate = Path(program_files_x86) / "Microsoft Visual Studio" / "Installer" / "vswhere.exe"
                if path_exists(candidate):
                    vswhere = str(candidate)
        checks["msvc"] = bool(cl or vswhere)
        toolchain["cl"] = cl
        toolchain["vswhere"] = vswhere
        if not checks["msvc"]:
            missing.append("msvc_missing")

        headers = [path for path in _sdk_header_candidates(sdk_root) if path_exists(path)]
        checks["nvenc_header"] = bool(headers)
        toolchain["nvenc_header"] = str(headers[0]) if headers else None
        if not headers:
            missing.append("nvenc_header_missing")

        runtimes = [path for path in _runtime_candidates(env) if path_exists(path)]
        checks["nvenc_runtime"] = bool(runtimes)
        toolchain["nvenc_runtime"] = str(runtimes[0]) if runtimes else None
        if not runtimes:
            missing.append("nvenc_runtime_missing")

        checks["nvidia_smi"] = bool(command_lookup("nvidia-smi"))
        if not checks["nvidia_smi"]:
            missing.append("nvidia_smi_missing")

    return {
        "ready": not missing,
        "missing": missing,
        "checks": checks,
        "toolchain": toolchain,
    }


def _load_result(path: Path) -> dict[str, Any] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    return value if isinstance(value, dict) else None


def run_experiment(
    output: Path,
    *,
    sdk_root: Path | None,
    root: Path | None = None,
    build_script: Path | None = None,
    binary: Path | None = None,
    platform_name: str | None = None,
    environment: Mapping[str, str] | None = None,
    command_lookup: Callable[[str], str | None] = shutil.which,
    path_exists: Callable[[Path], bool] = _file_exists,
    process_runner: Callable[..., subprocess.CompletedProcess[str]] = subprocess.run,
) -> tuple[int, dict[str, Any]]:
    output = Path(output)
    root = Path(root or Path(__file__).resolve().parent)
    env = dict(environment or os.environ)
    inspection = inspect_environment(
        sdk_root,
        platform_name=platform_name,
        environment=env,
        command_lookup=command_lookup,
        path_exists=path_exists,
    )
    if not inspection["ready"]:
        result = build_result(
            "unavailable",
            reason="missing local prerequisites: " + ", ".join(inspection["missing"]),
            checks=inspection["checks"],
            toolchain=inspection["toolchain"],
            metadata={"unavailable_reasons": inspection["missing"]},
        )
        write_result(output, result)
        return EXIT_UNAVAILABLE, result

    build_script = Path(build_script or root / "build.ps1")
    binary = Path(binary or output.parent / "nico-gpu-nvenc.exe")
    powershell = command_lookup("pwsh") or command_lookup("powershell") or "powershell.exe"
    build_command = [
        powershell,
        "-NoProfile",
        "-ExecutionPolicy",
        "Bypass",
        "-File",
        str(build_script),
        "-SdkRoot",
        str(sdk_root),
        "-OutputRoot",
        str(binary.parent),
    ]
    built = process_runner(
        build_command,
        cwd=str(root),
        env=dict(env),
        capture_output=True,
        text=True,
        check=False,
    )
    if built.returncode != 0 or not binary.is_file():
        result = build_result(
            "failed",
            reason="native_build_failed",
            toolchain=inspection["toolchain"],
            commands=[build_command],
            metadata={"build_returncode": built.returncode},
        )
        write_result(output, result)
        return EXIT_FAILURE, result

    native_command = [str(binary), "--output", str(output)]
    native = process_runner(
        native_command,
        cwd=str(root),
        env=dict(env),
        capture_output=True,
        text=True,
        check=False,
    )
    result = _load_result(output)
    if result is None:
        result = build_result(
            "failed",
            reason="native_result_missing",
            toolchain=inspection["toolchain"],
            commands=[build_command, native_command],
            metadata={"native_returncode": native.returncode},
        )
        write_result(output, result)
    return (EXIT_PASS if native.returncode == 0 and result.get("status") == "passed" else EXIT_FAILURE), result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--sdk-root", type=Path)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent)
    args = parser.parse_args(argv)
    code, result = run_experiment(args.output, sdk_root=args.sdk_root, root=args.root)
    print(json.dumps({"output": str(args.output), "status": result["status"], "reason": result["reason"]}))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
