import shutil
import subprocess
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "build-nico-timeline-compositor.sh"


def test_posix_build_script_selects_only_the_three_profiles_and_defaults_to_release():
    script = SCRIPT.read_text(encoding="utf-8")
    assert 'profile="release"' in script
    assert "    --profile)" in script
    assert 'release|release-thin|release-thin-one' in script
    assert 'cargo build --locked --profile "$profile"' in script


def test_posix_build_script_preserves_artifact_and_output_argument_contracts():
    script = SCRIPT.read_text(encoding="utf-8")
    assert '"$target_dir/$target/$profile/$binary_name"' in script
    assert "    --target)" in script
    assert "    --output-directory)" in script
    assert "    --for-go-embed)" in script
    assert 'internal/nicorender/timeline_payload' in script
    assert 'nico-compositord.bin' in script
    assert 'manifest.json' in script


def test_posix_build_script_records_separate_provenance_and_scrubs_build_overrides():
    script = SCRIPT.read_text(encoding="utf-8")
    assert 'source-inputs \\\n  --source-root "$repo_root"' in script
    assert 'build-provenance \\\n  --binary "$payload"' in script
    assert '--mode "$profile"' in script
    assert '--source-inputs "$source_inputs_file"' in script
    assert '--build-log "$build_log"' in script
    assert '--overrides-json "$overrides_file"' in script
    assert '--output "$output_dir/build-manifest.json"' in script
    assert 'collect_build_environment_overrides' in script
    assert 'collect_build_environment_overrides(os.environ)' in script
    assert 'CARGO_PROFILE_*' in script
    assert 'RUSTFLAGS' in script
    assert 'unset "$name"' in script
    assert '${name^^}' not in script
    assert 'upper_name=' not in script


def test_posix_script_parses_and_help_runs_when_bash_is_available():
    git_bash = Path("C:/Program Files/Git/bin/bash.exe")
    bash = str(git_bash) if git_bash.is_file() else shutil.which("bash")
    if not bash:
        pytest.skip("bash is unavailable; only static shell-contract checks ran")
    script_path = str(SCRIPT)
    if git_bash.is_file() and bash == str(git_bash):
        cygpath = Path("C:/Program Files/Git/usr/bin/cygpath.exe")
        if cygpath.is_file():
            converted = subprocess.run(
                [str(cygpath), "-u", script_path],
                capture_output=True,
                text=True,
                check=False,
                timeout=20,
            )
            assert converted.returncode == 0, converted.stdout + converted.stderr
            script_path = converted.stdout.strip()
    syntax = subprocess.run(
        [bash, "-n", script_path], capture_output=True, text=True, check=False, timeout=20
    )
    assert syntax.returncode == 0, syntax.stdout + syntax.stderr
    help_result = subprocess.run(
        [bash, script_path, "--help"],
        capture_output=True,
        text=True,
        check=False,
        timeout=20,
    )
    assert help_result.returncode == 0, help_result.stdout + help_result.stderr
    assert "--profile" in help_result.stdout


def test_posix_script_rejects_an_unsupported_profile_before_building():
    git_bash = Path("C:/Program Files/Git/bin/bash.exe")
    bash = str(git_bash) if git_bash.is_file() else shutil.which("bash")
    if not bash:
        pytest.skip("bash is unavailable; unsupported-profile execution was not tested")
    script_path = str(SCRIPT)
    if git_bash.is_file() and bash == str(git_bash):
        cygpath = Path("C:/Program Files/Git/usr/bin/cygpath.exe")
        if cygpath.is_file():
            converted = subprocess.run(
                [str(cygpath), "-u", script_path],
                capture_output=True,
                text=True,
                check=False,
                timeout=20,
            )
            assert converted.returncode == 0, converted.stdout + converted.stderr
            script_path = converted.stdout.strip()
    result = subprocess.run(
        [bash, script_path, "--profile", "invalid"],
        capture_output=True,
        text=True,
        check=False,
        timeout=20,
    )
    assert result.returncode == 2
    assert "Unsupported Cargo profile" in result.stderr
