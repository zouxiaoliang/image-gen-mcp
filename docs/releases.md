# Release packages

CI builds `darwin`, `linux`, and `windows` archives for `amd64` and `arm64` on push, pull request, or manual runs. Non-tag builds use `v0.0.0-ci` and remain as workflow artifacts for seven days. Cross-compilation does not replace runtime testing on each OS.

The source and workflow must be pushed before remote installs and releases work.

## Create a release

After reviewing and pushing the source, create a version tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Use `vMAJOR.MINOR.PATCH`, optionally with a prerelease suffix such as `-rc.1`. The tag is embedded in `image-gen-mcp -version`; ordinary source builds report `dev`.

After tests, the demo, and all builds pass, CI creates a **draft** GitHub Release with six archives and `SHA256SUMS`. Review notes, smoke-test target platforms, mark prereleases when appropriate, and publish the draft on GitHub. Draft assets are not public downloads. Only the release job receives `contents: write` permission.

An existing release causes the create step to fail instead of overwriting it. Update an existing draft deliberately or use a new version. Do not move published tags.

## Build locally

Requires Go 1.25+ and Python 3.9+, without Python dependencies:

```sh
python3 scripts/package-release.py --version v0.1.0 --goos darwin --goarch arm64
```

Archives contain the executable, example config, READMEs, docs, and the documented gbot patch. They exclude personal `config.yaml`, databases, task data, and keys. A `LICENSE` file is included if present; the maintainer still needs to choose a project license before describing this as licensed open source.

Builds use `CGO_ENABLED=0`, so SQLite needs no separate installation. macOS binaries are unsigned and not notarized.

## Verify a download

Download the archive and `SHA256SUMS` from the same release; compare the computed digest with the matching filename:

```sh
# Linux
sha256sum image-gen-mcp_v0.1.0_linux_amd64.tar.gz
# macOS
shasum -a 256 image-gen-mcp_v0.1.0_darwin_arm64.tar.gz
```

```powershell
# Windows
Get-FileHash .\image-gen-mcp_v0.1.0_windows_amd64.zip -Algorithm SHA256
```

Extract, copy `config.example.yaml` to `config.yaml`, configure your model, and run `-config /absolute/path/config.yaml -check`. Use `-version` to confirm the release. Checksums detect corruption; they do not provide signed provenance.
