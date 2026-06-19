from pathlib import Path
from urllib.request import urlopen
import hashlib, subprocess, sys, tempfile
from fontTools.ttLib import TTFont

SOURCE_URL = "https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/Variable/OTF/NotoSansCJKjp-VF.otf"
SOURCE_SHA256 = "AB2728702F90D2AE900309F299DC3C2B075010888A1A8A67FBD5B4C6AFF713A0"
LICENSE_URL = "https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/LICENSE"
LICENSE_SHA256 = "6A73F9541C2DE74158C0E7CF6B0A58EF774F5A780BF191F2D7EC9CC53EFE2BF2"
OUTPUTS = {400: "NotoSansCJKjp-Regular.otf", 500: "NotoSansCJKjp-Medium.otf", 600: "NotoSansCJKjp-SemiBold.otf"}

def main() -> None:
    output_dir = Path(__file__).resolve().parents[2] / "assets" / "fonts"
    output_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="imagepad-noto-") as temp:
        source = Path(temp) / "NotoSansCJKjp-VF.otf"
        source.write_bytes(urlopen(SOURCE_URL, timeout=60).read())
        actual = hashlib.sha256(source.read_bytes()).hexdigest().upper()
        if actual != SOURCE_SHA256:
            raise SystemExit(f"font hash mismatch: {actual}")
        for weight, name in OUTPUTS.items():
            cmd = [sys.executable, "-m", "fontTools.varLib.instancer", str(source), f"wght={weight}", f"--output={output_dir / name}"]
            # The JP subset has named Axis Values for 400 and 500 only; --update-name-table
            # requires corresponding STAT Axis Values, so we skip it for 600 (SemiBold).
            if weight in (400, 500):
                cmd.append("--update-name-table")
            subprocess.run(cmd, check=True)
        license_bytes = urlopen(LICENSE_URL, timeout=60).read()
        if hashlib.sha256(license_bytes).hexdigest().upper() != LICENSE_SHA256:
            raise SystemExit("license hash mismatch")
        (output_dir / "OFL.txt").write_bytes(license_bytes)
    # Verify each output's weight class
    for weight, name in OUTPUTS.items():
        with TTFont(output_dir / name) as f:
            usWC = f["OS/2"].usWeightClass
            if usWC != weight:
                raise SystemExit(f"{name}: expected usWeightClass={weight}, got {usWC}")

if __name__ == "__main__":
    main()
