# image-gen-mcp

**Generate and edit images from your MCP client. One Go binary, multiple providers, persistent tasks.**

[中文](README.zh-CN.md) · [Quick start](#quick-start) · [No-key demo](docs/demo.md) · [Releases](https://github.com/zouxiaoliang/image-gen-mcp/releases) · [Configuration reference 中文](docs/guide.zh-CN.md)

[![CI](https://github.com/zouxiaoliang/image-gen-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/zouxiaoliang/image-gen-mcp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/zouxiaoliang/image-gen-mcp.svg)](https://pkg.go.dev/github.com/zouxiaoliang/image-gen-mcp)

![MCP generation, editing and request deduplication demo](docs/assets/demo.gif)

*Real MCP subprocess, local fixture replay, no API key. Saved images come from a verified BitAPI generation/editing run; this is not a live model call. [Reproduce it](docs/demo.md).*

## Why use it?

- **Create and edit in one workflow.** Generate from text, supply reference images, or request edits from your MCP client.
- **Choose your provider.** OpenAI Images, OpenAI-compatible gateways, and native Gemini `generateContent`, with named model profiles.
- **Get previews and originals.** MCP image content for clients that support it, plus local file paths over stdio or download URLs over HTTP.
- **Keep track of long requests.** SQLite-backed tasks, polling, cancellation, restart recovery, and optional image expiry.
- **Reuse a request without submitting it twice.** The same `request_id` and parameters return the stored task. Uncertain results are marked `unknown`, never automatically retried.

## Quick start

### 1. Install

With Go 1.25 or newer:

```sh
go install github.com/zouxiaoliang/image-gen-mcp/cmd/image-gen-mcp@latest
```

The executable goes into `GOBIN`, or `$(go env GOPATH)/bin` by default. Add that directory to `PATH`, or use the executable's absolute path.

**Without Go:** download an archive from [Releases](https://github.com/zouxiaoliang/image-gen-mcp/releases). Packages include the executable, example configuration, and docs. The workflow builds macOS, Linux, and Windows for amd64 and arm64, with `SHA256SUMS`. Windows uses `image-gen-mcp.exe`; macOS packages are currently unsigned.

From a source checkout:

```sh
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
```

### 2. Configure a model

Save [config.example.yaml](config.example.yaml) as `config.yaml`. `go install` does not install configuration. A minimal gateway profile:

```yaml
default_model: image
output_dir: ./data
allowed_input_dirs: [./input, ./data/images]
models:
  image:
    provider: openai-compatible
    base_url: https://YOUR_GATEWAY_HOST/v1
    token: YOUR_API_KEY
    model: YOUR_IMAGE_MODEL
    capabilities: [generate, edit]
    max_images: 1
    max_input_images: 1
```

Replace the three `YOUR_...` values and only declare capabilities your provider supports. Use `provider: openai` for OpenAI Images, or the Gemini profile in the full example. To read credentials from the environment, replace `token` with `token_env: IMAGE_API_KEY`.

```sh
image-gen-mcp -config /absolute/path/config.yaml -check
```

This validates configuration locally, not credentials or model availability. YAML paths are relative to the configuration file. Real generation uses your provider account and may incur charges.

### 3. Connect your MCP client

For clients accepting `mcpServers` JSON, replace both paths:

```json
{
  "mcpServers": {
    "image-gen": {
      "command": "/absolute/path/image-gen-mcp",
      "args": ["-config", "/absolute/path/config.yaml", "-transport", "stdio"]
    }
  }
}
```

On Windows, use the `.exe` path and escape backslashes, for example `C:\\tools\\image-gen-mcp.exe`. Client configuration formats and image rendering vary. MCP SDK stdio/HTTP clients and the [gbot integration](docs/gbot.md) have been tested; other clients have not been individually verified.

Ask your agent:

> List the available image models. Generate a red square on a white background, using request ID `first-image-001`. Poll until complete, then show the preview and original file path.

Then:

> Edit that image: turn the red square into a blue circle and keep the white background. Use a new request ID for this edit.

Success returns `succeeded`, an image preview, and original metadata. If your client cannot display MCP images, open the returned original file. Editing requires the model's `edit` capability.

## Tools

| Tool | Purpose |
| --- | --- |
| `list_image_models` | Discover model aliases, capabilities, parameters, and credential presence |
| `generate_image` | Generate from a text prompt |
| `edit_image` | Edit using reference images and an optional PNG mask |
| `get_image_task` | Poll status and retrieve completed images |
| `cancel_image_task` | Cancel queued work or stop local waiting |

Reuse the request ID for the same operation; use a new ID for an intentional new generation. Cancellation after submission does not guarantee upstream cancellation or a refund.

## HTTP and provider support

```sh
image-gen-mcp -config /absolute/path/config.yaml -transport http
```

The default endpoint is `http://127.0.0.1:8080/mcp`. Remote access requires an MCP bearer token and a configured public URL; use a reverse proxy for HTTPS. Each instance needs its own output directory. See the [reference 中文](docs/guide.zh-CN.md) for parameters, authentication, input restrictions, and retention.

| Provider | Local protocol tests | Real service verification |
| --- | --- | --- |
| OpenAI Images | Passed | Not yet verified |
| OpenAI-compatible gateway | Passed | BitAPI generation and reference editing verified |
| Gemini | Passed | Not yet verified |

Gemini masks are unsupported. The tested BitAPI mask result changed the background outside the mask to transparent; do not assume exact preservation. [Acceptance evidence 中文](docs/acceptance.md).

## Development

```sh
go test -race ./...
go vet ./...
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
go run ./examples/demo -server ./bin/image-gen-mcp
```

The demo and default tests use local mocks, without API keys. CI checks formatting, dependencies, static analysis, race tests, the demo, and six release builds. [Release instructions](docs/releases.md) · [Contributing](CONTRIBUTING.md).

If this helps your workflow, a star helps other MCP users discover it. Bug reports with sanitized configuration and reproduction steps are especially useful.
