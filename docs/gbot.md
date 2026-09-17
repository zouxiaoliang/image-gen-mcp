# gbot 接入

已验证的 gbot 版本为 `f75609b9aaf4f53583f49be6553abcbbd192fdf2`。它使用独立的 `sdk/mcpclient` 模块连接 MCP，支持 stdio 与 Streamable HTTP。下文 `/absolute/path/gbot` 表示你的 gbot 源码目录。

## 配置

将 [stdio 配置片段](../examples/gbot-stdio.json) 中的 `tools.mcpServers.image-gen` 合并到 gbot 配置的对应位置，保留原有配置项。先将片段中的程序和 `config.yaml` 路径替换为你自己的绝对路径；片段不包含上游密钥。gbot 的配置顶层需要 `tools`，不同于 README 中通用客户端的顶层 `mcpServers` 示例。

默认工具调用超时 30 秒；图片服务先短暂等待，未完成时返回任务 ID，因此不需要让客户端阻塞整个生成过程。gbot 中的工具名会变成 `mcp_image_gen_generate_image`、`mcp_image_gen_get_image_task` 等。

HTTP 方式对应服务器条目可改为：

```json
{
  "enabled": true,
  "type": "streamable_http",
  "url": "http://127.0.0.1:8080/mcp",
  "toolTimeout": 30
}
```

如果图片服务设置了独立的 MCP 访问令牌，条目还需增加 `headers.Authorization`，值为 `Bearer <MCP 访问令牌>`。上游图片 API Key 仍仅配置在图片服务的 YAML 中。

## 图片内容补丁（已应用）

gbot 原先的 `sdk/mcpclient/mcpclient.go` 会将 MCP 图片转换为 `[Image content: image/png]` 文本，丢弃图片数据。原始版本可以获取任务信息和原图位置，但不能将 MCP 预览直接传给视觉模型。

[gbot 补丁](../patches/gbot-mcp-images.patch) 保留图片为 `image_url` 内容块，复用 gbot 已有的 `__GBOT_TOOL_CONTENT__:` 格式和图片尺寸策略，由 `sdk/agentloop` 的 `parseStructuredToolOutput` 接收。文字、任务元数据与错误信息同时保留；纯文本工具结果保持原有格式。该补丁针对 gbot 的 agentloop；单独使用 `sdk.GenerateText` 的调用方仍需支持多模态工具结果。

补丁只涉及三个文件：

- `sdk/mcpclient/mcpclient.go`：保留工具结果中的图片。
- `sdk/mcpclient/image_result_test.go`：验证多个图片、文本顺序、元数据、错误标记和纯文本兼容性。
- `sdk/mcpclient/image_gen_mcp_integration_test.go`：可选的图片 MCP 实际进程测试，并验证 agentloop 将预览转发到下一轮模型请求。

2026-09-17 已在临时 SDK 副本中验证：模块全套测试开启 `-race` 通过；连接本项目实际二进制的 stdio/HTTP 测试均通过，能发现五个工具、解码 MCP 预览，并读取原图。上游使用本机模拟服务，不读取用户配置或真实密钥，不产生生成费用。

随后按用户“应用到 gbot”的确认，将补丁应用到本机 `/absolute/path/gbot`。只修改上述三个 SDK 文件，未修改其运行配置；已运行的旧版 gbot 进程需使用重新构建的程序才能加载此变更。

应用后已在原项目运行 `go test -race ./... -count=1 -v`，并通过 `IMAGE_GEN_MCP_BINARY` 启用实际 stdio/HTTP 接入测试，全部通过（包测试耗时 3.335 秒）；`git diff --check` 通过。预览可解码，原图路径和 HTTP 下载均有效。

2026-09-18 补充 agentloop 集成验证，两种传输均通过（3.162 秒）：本机模拟模型先返回生成工具调用，gbot 实际执行该 MCP 工具，随后发出的模型 HTTP 请求包含与 MCP 预览一致的图片 data URL。该验证经过真实 Agent 循环和模型请求序列化，避免只在 SDK 返回值层面检查图片。模拟生成接口和模拟模型均在本机，没有真实 API 费用。

在 gbot 的 `sdk/mcpclient` 目录可复验：

```sh
go test -race ./...
IMAGE_GEN_MCP_BINARY=/absolute/path/image-gen-mcp/bin/image-gen-mcp \
  go test -race ./... -run '^TestImageGenMCPTransports$' -count=1 -v
```

上述测试验证协议、图片传递以及 Agent 发出的模型请求，不评价真实视觉模型对图片的理解能力。
