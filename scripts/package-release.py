"""Build a standalone release archive with only public configuration and docs."""

import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="Release tag, e.g. v0.1.0")
    parser.add_argument("--goos", required=True, choices=["linux", "darwin", "windows"])
    parser.add_argument("--goarch", required=True, choices=["amd64", "arm64"])
    parser.add_argument("--output", default="dist")
    args = parser.parse_args()
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", args.version):
        parser.error("version must be a v-prefixed release version")

    root = Path(__file__).resolve().parents[1]
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    name = f"image-gen-mcp_{args.version}_{args.goos}_{args.goarch}"
    with tempfile.TemporaryDirectory(prefix="image-gen-mcp-release-") as tmp:
        bundle = Path(tmp) / name
        bundle.mkdir()
        binary = bundle / ("image-gen-mcp.exe" if args.goos == "windows" else "image-gen-mcp")
        env = dict(os.environ, CGO_ENABLED="0", GOOS=args.goos, GOARCH=args.goarch)
        subprocess.run([
            "go", "build", "-trimpath", "-buildvcs=false",
            "-ldflags", f"-s -w -X github.com/zouxiaoliang/image-gen-mcp/internal/imagegen.Version={args.version}",
            "-o", str(binary), "./cmd/image-gen-mcp",
        ], cwd=root, env=env, check=True)
        binary.chmod(0o755)
        for public_file in ["README.md", "README.zh-CN.md", "CONTRIBUTING.md", "config.example.yaml"]:
            shutil.copy2(root / public_file, bundle / public_file)
        for directory in ["docs", "patches"]:
            shutil.copytree(root / directory, bundle / directory)
        (bundle / "examples").mkdir()
        shutil.copy2(root / "examples" / "gbot-stdio.json", bundle / "examples" / "gbot-stdio.json")
        if (root / "LICENSE").is_file():
            shutil.copy2(root / "LICENSE", bundle / "LICENSE")

        if args.goos == "windows":
            archive = output / f"{name}.zip"
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as target:
                for path in sorted(bundle.rglob("*")):
                    if path.is_file():
                        target.write(path, path.relative_to(Path(tmp)))
        else:
            archive = output / f"{name}.tar.gz"
            with tarfile.open(archive, "w:gz") as target:
                target.add(bundle, arcname=name)
        checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
        (output / f"{archive.name}.sha256").write_text(
            f"{checksum}  {archive.name}\n", encoding="utf-8"
        )
        print(archive)


if __name__ == "__main__":
    main()
