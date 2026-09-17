# 完整使用手册

[返回中文首页](../README.zh-CN.md) · [English](../README.md)

使用 Go 实现的图片 MCP 服务，为 gbot、pi、Codex 等支持 MCP 的 Agent 提供文生图、参考图生成和图片编辑能力。

- 官方 MCP Go SDK；stdio 和 Streamable HTTP 两种启动模式。
- OpenAI Images、OpenAI 格式兼容网关、Gemini `generateContent` 三类适配器。
- 多模型别名；上游地址、模型名和密钥由 YAML 配置，也可选择环境变量方式。
- 工具结果包含 PNG 预览图、结构化任务信息，以及完整原图的路径或下载 URL。
- SQLite 持久化、后台任务、请求去重、取消、可选过期清理。

本机测试已覆盖实际 MCP 客户端的 stdio/HTTP 调用。2026-09-17 使用 YAML 中的密钥实测 `https://api.bitapi.codes/v1`、`gpt-image-2.5`：文生图和参考图编辑效果通过，蒙版请求成功返回绿色星形，但蒙版外的白色背景变为透明，未通过区域保留效果验收。OpenAI 官方服务及 Gemini 尚未实测；能力与参数允许值需要按实际模型调整。

OpenAI 官方和 Gemini 按已确认的零成本方案验收：使用本机模拟上游验证适配器，保留“官方服务未实测”和“真实 Gemini 未验证”标记。无需购买这两家的服务或填写真实密钥即可完成适配器开发验收；模拟结果不代表真实模型的生成质量或可用性。BitAPI 兼容网关采用上述真实调用结果验收。

## 安装与启动

需要 Go 1.25 或以上版本。

### 使用 go install 安装

```sh
go install github.com/zouxiaoliang/image-gen-mcp/cmd/image-gen-mcp@latest
```

可执行文件安装到 `GOBIN` 指定的目录；未设置时，默认安装到 `$(go env GOPATH)/bin`。将该目录加入 `PATH`，或在 MCP 客户端的 `command` 中填写可执行文件的绝对路径。

`go install` 只安装程序，不会安装配置文件。下载仓库中的 [config.example.yaml](../config.example.yaml)，保存为 `config.yaml` 并按下文填写模型配置，然后运行：

```sh
image-gen-mcp -config /absolute/path/config.yaml -check
image-gen-mcp -config /absolute/path/config.yaml -transport stdio
# 使用 HTTP 时：
image-gen-mcp -config /absolute/path/config.yaml -transport http
```

### 从源码构建

```sh
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，至少填写一个真实模型，设置 `default_model` 指向它。`base_url` 包含 API 版本前缀，例如 OpenAI 的 `https://api.openai.com/v1`，不要填写完整的 `/images/generations` 路径。Gemini 使用 `https://generativelanguage.googleapis.com/v1beta`。

直接在配置文件中填写上游密钥即可，不需要设置环境变量：

```yaml
models:
  openai:
    provider: openai
    base_url: https://api.bitapi.codes/v1
    model: gpt-image-2.5
    token: "填写你的 API Key"
    # 其他能力和参数设置参照 config.example.yaml。
```

上例使用本次实测的兼容网关与模型；其他服务需填写其对应地址和模型名。三类适配器都支持 `token`。如果想保留环境变量方式，也可以省略 `token`、改用 `token_env: OPENAI_API_KEY`；两者都填写时，以非空 `token` 为准。本地 `config.yaml` 已在 `.gitignore` 中。

`list_image_models` 仅返回密钥是否配置的布尔值，不返回密钥内容。`token_env` 指定的环境变量未设置时，`token_configured` 为 `false`；调用该模型会明确报错，不会切换到其他模型。

```sh
# 校验配置；不联网、不检查账号或模型可用性。
./bin/image-gen-mcp -config config.yaml -check

# stdio：由 Agent 启动；stdout 仅输出 MCP 协议消息，日志写 stderr。
./bin/image-gen-mcp -config config.yaml -transport stdio

# Streamable HTTP：MCP 端点为 /mcp。
./bin/image-gen-mcp -config config.yaml -transport http
```

相对路径均相对于配置文件，而不是进程工作目录。一个输出目录只能由一个服务进程占用，避免重复消费任务；stdio 与 HTTP 实例同时运行时请分别配置 `output_dir`。

`-check` 与启动使用相同的配置校验，不创建数据库、不绑定端口。HTTP 正式启动会先绑定监听端口，再恢复任务；配置错误或端口占用不会触发后台生成。

## Agent 接入

stdio 客户端配置示例（具体配置文件位置由客户端决定）：

```json
{
  "mcpServers": {
    "image-gen": {
      "command": "/absolute/path/image-gen-mcp/bin/image-gen-mcp",
      "args": ["-config", "/absolute/path/image-gen-mcp/config.yaml", "-transport", "stdio"]
    }
  }
}
```

使用配置文件中的 `token` 时，Agent 无需传递上游密钥环境变量。只有选择 `token_env` 方式时，才需要让 Agent 子进程继承相应环境变量。

HTTP 客户端使用 Streamable HTTP，连接 `http://127.0.0.1:8080/mcp`。配置了 MCP 访问令牌时，每个 MCP 请求以及原图下载请求均需带：

```text
Authorization: Bearer <IMAGE_MCP_TOKEN 的值>
```

服务默认监听本机。远程部署时修改 `http.listen` 与 `http.public_url`，设置 `http.token_env` 指定的独立访问令牌；对外 HTTPS 由反向代理提供。`public_url` 必须是无路径的外部 origin，代理需要保留这个 Host。服务校验 Host 和 Origin，不开放跨站浏览器调用。

在 gbot 中，使用已有 MCP 客户端列举工具、发起 `tools/call`，并同时处理：

- `structuredContent`：任务 ID、状态、图片元数据和错误。
- `content` 中的 `image`：供模型查看的 PNG 预览，MCP JSON 中为 Base64。
- `content` 中的 `text`：相同结果的 JSON 文本，便于不处理结构化结果的客户端读取。

是否能“当场查看”图片还取决于客户端是否将 MCP image 内容传给具备视觉能力的模型。原图下载 URL 需要客户端携带令牌，不会把令牌放进 URL。

本机 gbot 原先会将 MCP 图片替换为文字占位符。已按用户确认应用图片传递补丁，保留预览供其 agentloop 处理；具体配置、兼容范围和验证步骤见 [gbot 接入说明](gbot.md)。

## 工具

| 工具 | 用途 |
| --- | --- |
| `list_image_models` | 查询别名、能力、可用参数、数量限制和密钥配置状态 |
| `generate_image` | 文字生成图片 |
| `edit_image` | 参考图生成、文字编辑、可选蒙版编辑 |
| `get_image_task` | 查询状态；成功时包含图片预览及原图位置 |
| `cancel_image_task` | 取消排队或本地执行；已提交的上游可能继续生成和计费 |

文生图调用参数：

```json
{
  "prompt": "为旅行博客生成一张清晨山谷背景图，不要文字",
  "model": "openai",
  "size": "1536x1024",
  "output_format": "png",
  "n": 1,
  "request_id": "travel-background-v1"
}
```

参考图或编辑调用参数：

```json
{
  "prompt": "保留人物姿态，把背景改成浅蓝色",
  "model": "openai",
  "images": [{"path": "/absolute/path/input/person.png"}],
  "request_id": "person-background-v1"
}
```

每个输入图片只允许一种来源：

```json
{"path": "/absolute/server/path/input.png"}
{"url": "https://example.com/reference.png"}
{"data": "原始图片的Base64字符串，不带data:前缀"}
```

`path` 必须是 MCP 所在机器的绝对路径，且在 `allowed_input_dirs` 内；符号链接不能跳出允许目录。远程 Agent 的本机路径不等于服务器路径，需改用 URL 或 Base64 数据。URL 下载仅允许公共网络地址，私有地址和回环地址不会下载；该限制也适用于上游返回的图片 URL。管理员配置的上游 `base_url` 可以指向内部网关。

需要局部编辑时增加 `mask`，来源格式与图片相同。首版要求蒙版与第一张原图都是 PNG、尺寸相同，且蒙版包含完全透明的像素；透明区域表示请求编辑的区域。蒙版是模型提示，不保证精确保持边界。Gemini 适配器没有实现蒙版，会在提交前拒绝这种调用。

图片支持 PNG、JPEG、WebP。默认单图最大 20 MiB、最多 4000 万像素；预览长边默认最大 768 像素，原图字节不变。输入文件和 URL 在任务开始准备时读取，因此排队期间应保持它们可访问且内容不变。

## 模型能力与参数

配置声明模型的 `capabilities`，可取 `generate`、`edit`、`mask`。这些声明是管理员提供的兼容契约，不会自动探测真实服务。

| 适配器 | 通用参数映射 | 图片输出 |
| --- | --- | --- |
| OpenAI / 兼容网关 | `size`、`quality`、`output_format`、`n` | `data[].b64_json`，或兼容服务返回的 `data[].url` |
| Gemini | `size` → `imageConfig.imageSize`，`aspect_ratio` → `imageConfig.aspectRatio` | `candidates[].content.parts[].inlineData` |

`parameters` 中列出的参数才允许调用，枚举列表限制可用值；空列表表示允许任意字符串。Gemini 的 `size` 是分辨率档位，可以与 `aspect_ratio` 同时使用；首版仅支持 `n=1`，不把多张请求拆成多次付费调用。

`extra` 用于服务商扩展参数，需要配置 `extensions` 的类型和可选字符串枚举。适配器有固定白名单，不能通过扩展覆盖模型、地址、密钥或输入图片：

- OpenAI：`background`、`moderation`、`output_compression`、`input_fidelity`、`response_format`、`style`、`user`。
- Gemini：`temperature`、`topP`、`topK`、`seed`。

服务商专属参数也可能只适用于部分模型或操作，例如 `input_fidelity` 用于编辑；配置应按实际接口精确收窄。未支持的字段会明确报错，不静默丢弃，不自动更换模型。

## 任务、去重与计费

默认并发 2、等待队列 20、任务执行超时 10 分钟。提交最多等待 `wait_timeout`（默认 3 秒，最多 30 秒），随后立即返回当前任务状态。客户端断开不会取消后台任务。

| 状态 | 含义 |
| --- | --- |
| `queued` | 已持久化，等待工作线程 |
| `preparing` | 正在读取和检查输入，尚未提交生成请求 |
| `running` | 已进入上游提交阶段 |
| `succeeded` | 结果已保存，附带预览及原图元数据 |
| `failed` | 输入准备失败、配置错误或上游明确拒绝 |
| `cancelled` | 在上游提交前取消 |
| `unknown` | 可能已生成并计费，无法确认结果；不得自动重试 |
| `expired` | 保留期限已到，图片已清理，去重记录保留 |

调用后若状态未结束，使用 `get_image_task` 查询，建议间隔 1～3 秒。`failed` 和 `unknown` 结果同时设置 MCP `isError`，结构化结果仍包含任务信息。

同一 `request_id` 与相同参数会返回原任务，即使重启也不会重复生成。相同 ID 配不同参数或模型配置会返回 `idempotency_conflict`。不提供 ID 的每次调用都视为新任务。去重以调用参数为准，不保证同一个 URL 或文件路径的内容未被外部修改。

提交后连接中断、超时、上游 5xx 或结果无法读取时，任务进入 `unknown`，没有自动重试或服务商回退。取消已提交任务只能停止本地等待，不能保证上游停止或退款。确实要重新生成时，由调用方明确决定并使用新的 `request_id`。

重启后，已完成结果可查询；排队和准备中的任务继续处理；进入上游提交阶段的任务标记 `unknown`。任务保存不含密钥的模型配置快照，执行时再从当前配置文件的 `token` 或指定环境变量读取密钥。只更换密钥不会改变任务去重标识；如果服务商、地址或模型已改变，旧排队任务会明确报配置错误，避免将当前密钥用于旧请求。

## 文件与保留期限

`output_dir` 内保存 SQLite 数据库、任务原图和预览。生成结果保存在 `images/<task_id>/`。数据库通过独立 SQLite 排他锁避免两个进程同时消费同一目录，崩溃后操作系统释放锁。

默认 `retention: 0s` 不删除图片。设置例如 `168h` 后，从任务结束时起保留七天，结果提供 `expires_at`，服务每分钟清理一次；HTTP 到期后立即拒绝下载。任务与请求去重记录仍保留，避免文件过期导致重复生成。排队任务中的输入数据会暂存到 SQLite；任务结束后清除请求负载，数据库自身的历史页回收由 SQLite 管理。

## 验证

GitHub Actions CI 在 push、pull request 和手动触发时运行，使用 `go.mod` 指定的 Go 版本，在 Ubuntu 上检查 Go 格式、依赖完整性、静态分析、竞态测试、构建及示例配置。CI 不需要 API 密钥，不运行真实服务验收。

```sh
go test -race ./...
go vet ./...
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
./bin/image-gen-mcp -config config.example.yaml -check
```

测试使用模拟上游，覆盖请求字段、JSON/multipart、Gemini 图片解析、URL 下载、输入文件边界、蒙版、预览、去重、队列、取消、重启恢复、过期清理，以及真实 MCP 客户端的 stdio 和 HTTP 调用。不会使用真实密钥或产生生成费用。

此外包含 HTTP 客户端断开后继续执行的测试，以及强制终止 stdio 子进程后恢复的测试：已提交任务变为 `unknown`，排队任务继续执行，两个请求都不重复提交。

### Gemini 零成本验收

以下测试自动启动本机模拟接口，使用临时配置和测试密钥，不读取你的 `config.yaml`，也不调用真实服务：

```sh
go test -race ./internal/imagegen -run '^(TestGeminiProtocol|TestGeminiResolutionAndRatio|TestEditWorkflowsThroughMCP|TestInlineTokenFromYAML)$/^gemini$' -count=1 -v
```

覆盖 Gemini 请求路径与鉴权、参考图片编码、图片响应解析、分辨率与比例参数、YAML 文件密钥、MCP 生成与编辑返回、三种参考图来源，以及不支持的蒙版在提交前被拒绝。测试图片为固定夹具，不用于评价模型的生成或编辑效果。

### 可选真实服务验收

`TestLiveProviders` 默认跳过。在配置文件中填写真实 `token`（或选择环境变量方式）后，可显式运行：

```sh
export IMAGE_MCP_LIVE_CONFIG="$PWD/config.yaml"
export IMAGE_MCP_LIVE_MODELS='openai'
export IMAGE_MCP_LIVE_RUN_ID='acceptance-001'
go test ./internal/imagegen -run '^TestLiveProviders$' -count=1 -timeout=90m -v
```

以上环境变量只用于选择验收配置、模型别名和验收 ID，并不用于保存密钥。模型别名必须与配置一致；上例只选择已配置的 `openai` 别名。只有准备实测其他服务时才追加对应别名（逗号分隔）；省略 `IMAGE_MCP_LIVE_MODELS` 时检查所有已配置模型。测试会先检查全部选中模型的配置与密钥，再针对声明的 `generate`、`edit`、`mask` 能力各调用一次，并通过 MCP 查询结果、检查内嵌预览和原图文件。这些是真实付费请求，不属于 Gemini 零成本验收。

验收数据保存在 `<output_dir>/live-verification/`，与运行中的服务分开，且不自动过期。**重跑时保留同一个 `IMAGE_MCP_LIVE_RUN_ID`、配置和验收数据目录**，已完成任务会复用；遇到 `unknown` 时测试失败，不自动重新生成。更换验收 ID 表示明确发起新的一轮生成。

测试日志提供任务 ID、状态和原图路径；需要查看原图确认提示词编辑效果。真实模型或网关限制不匹配时，应调整相应模型配置，不能将模拟测试通过当作真实兼容性已验收。

真实验收需为选定模型提供本地配置和有效密钥，验证文生图、参考图、所支持的蒙版/参数，以及 Agent 对图片内容的展示。

逐项验收范围与当前证据见 [验收记录](acceptance.md)。

接口参考：[OpenAI 图片生成与编辑](https://developers.openai.com/api/docs/guides/image-generation)、[Gemini 图片生成](https://ai.google.dev/gemini-api/docs/image-generation)、[Google 官方 API Discovery](https://generativelanguage.googleapis.com/$discovery/rest?version=v1beta)、[官方 MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)。
