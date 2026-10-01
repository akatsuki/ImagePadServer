import re
import tomllib
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
CARGO_TOML = ROOT / "gpu" / "nico-compositord" / "Cargo.toml"
BUILD_SCRIPT = ROOT / "scripts" / "build-nico-timeline-compositor.ps1"


def _profiles():
    with CARGO_TOML.open("rb") as source:
        return tomllib.load(source)


def test_release_profile_is_unchanged_and_thin_profiles_are_single_factor():
    profiles = _profiles()
    assert "profile" in profiles
    assert "release" not in profiles["profile"]
    assert profiles["profile"]["release-thin"] == {
        "inherits": "release",
        "lto": "thin",
    }
    assert profiles["profile"]["release-thin-one"] == {
        "inherits": "release-thin",
        "codegen-units": 1,
    }


def test_powershell_build_selector_defaults_to_release_and_maps_profile_output():
    script = BUILD_SCRIPT.read_text(encoding="utf-8")
    assert re.search(r'\[string\]\$Profile\s*=\s*"release"', script)
    assert all(value in script for value in ("release", "release-thin", "release-thin-one"))
    assert re.search(r'ValidateSet\([^)]*release[^)]*release-thin[^)]*release-thin-one', script)
    assert re.search(r'"--profile"\s*,\s*\$Profile', script)
    assert re.search(r'Join-Path\s+\$Profile\s+\$cargoBinary', script)


def test_powershell_build_selector_rejects_unlisted_profiles():
    script = BUILD_SCRIPT.read_text(encoding="utf-8")
    selector = re.search(r'\[ValidateSet\(([^)]*)\)\]\s*\[string\]\$Profile', script)
    assert selector, "Profile must be constrained to the three supported values"
    values = re.findall(r'"([^"]+)"', selector.group(1))
    assert values == ["release", "release-thin", "release-thin-one"]
