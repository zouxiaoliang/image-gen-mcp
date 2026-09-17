# Reproduce the demo / 复现演示

The README animation is rendered from a successful execution of `examples/demo`, not a recording of a particular agent UI. It runs the real server binary and MCP SDK over stdio. A loopback HTTP server replays saved PNG fixtures; no personal configuration or paid API is used.

首页 GIF 来自真实运行的 MCP 演示记录，并非某个 Agent 界面录屏。演示通过 stdio 启动真实服务程序，仅使用本地 HTTP 模拟图片上游，不读取个人配置或调用付费接口。

From a source checkout with Go 1.25+:

```sh
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
go run ./examples/demo -server ./bin/image-gen-mcp
```

On Windows, build `bin/image-gen-mcp.exe` and pass that path to `-server`.

The demo verifies each result before reporting success:

1. Start the MCP server and connect over stdio.
2. Discover five tools and query model configuration.
3. Generate, poll completion, verify the PNG preview and original bytes.
4. Edit with the generated reference image and verify the result.
5. Resubmit the original request ID; verify the same task and only two upstream calls in total.

Temporary configuration and task data are removed on exit. To regenerate the animation, first record a successful run:

```sh
go run ./examples/demo -server ./bin/image-gen-mcp -record docs/assets/demo-recording.json
python3 -m pip install Pillow
python3 scripts/render-demo.py
```

Pillow is only needed for rendering artwork. GIF timing is adjusted for readability, not a performance benchmark.

## Image provenance

`assets/generated.png` and `assets/edited.png` are 768-pixel previews from the real BitAPI / `gpt-image-2.5` verification on 2026-09-17: a red square on white, then an edit to a blue circle. Original images were 1254×1254. [Task IDs and evidence](acceptance.md).

Replaying these images does not re-test provider availability, quality, or prompt adherence. The demo excludes the mask result that failed background preservation.
