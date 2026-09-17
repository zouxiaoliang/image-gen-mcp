# 功能验收记录

范围来自需求访谈 Q1–Q20；不将尚未配置的真实服务验收视为完成。下表的“本地通过”仅表示实现及本机协议/集成测试通过，不代表真实上游已生成图片。

2026-09-17 用户确认采用 Gemini 零成本方案：以本机模拟接口完成 Gemini 开发验收，真实 Gemini 验证改为可选后续事项，不再要求购买服务或提供真实密钥作为交付前提。文档继续保留“真实 Gemini 未验证”标记。

2026-09-18 用户进一步确认 OpenAI 官方也按模拟测试验收，保留“官方服务未实测”标记。当前交付要求为：两类官方适配器完成模拟测试、已有 BitAPI 网关完成真实调用、gbot 的图片内容传递链路通过集成验证。其他 Agent 使用标准 MCP 协议接入，不宣称每个客户端都已单独实测。

| 已确认需求 | 实现及验收证据 | 当前结论 |
| --- | --- | --- |
| Q1、Q17：Agent 使用标准 MCP，五个工具 | 官方 Go SDK；`TestMCPOverStdio`、`TestMCPOverHTTP` 验证初始化、工具枚举、调用、结构化数据、内嵌 PNG | 本地通过；不依赖特定 Agent 私有协议 |
| Q2、Q5：OpenAI、兼容网关、Gemini 原生适配 | `TestOpenAIProtocols`、`TestCompatibleURLResult`、`TestGeminiProtocol` 验证请求与返回格式；`TestEditWorkflowsThroughMCP` 覆盖三类适配器 | 本地通过；BitAPI 网关已实测；Gemini 采用模拟验收，真实 Gemini 未验证；OpenAI 官方未实测 |
| Q3、Q6：stdio 与 Streamable HTTP，本机默认、远程令牌 | `TestMCPOverHTTP` 验证下载鉴权及 Host/Origin；配置阻止无令牌的非回环监听 | 实现完成；本机 HTTP/stdio 通过 |
| Q4、Q9：文生图、参考图、文字编辑及可选蒙版 | MCP 生成和编辑工作流；蒙版校验；Gemini 不支持蒙版时在上游调用前拒绝 | 本地通过；BitAPI 文生图和参考编辑效果通过；蒙版请求成功但未保留区域外背景 |
| Q7、Q15：当场查看预览，并取得原图 | `TestPreviewResizesWithoutChangingOriginal`；HTTP 鉴权下载逐字节验证；stdio 返回本地原图；gbot 实际 agentloop 下一轮模型请求保留图片 | 本地及 gbot 链路通过；实际模型视觉理解能力由所选模型决定 |
| Q8：文件、URL、Base64 三种输入 | `TestEditWorkflowsThroughMCP` 三种来源均经 MCP 提交；`TestInputBoundariesAndMask` 覆盖文件目录、符号链接、私网 URL、尺寸边界 | 本地通过 |
| Q10：Go 单个服务程序 | `cmd/image-gen-mcp`、`go build`；启动参数选择传输方式 | 实现完成 |
| Q11（按后续要求更新）：YAML 多模型、默认别名、文件密钥、可选环境变量、能力查询 | `TestInlineTokenFromYAML` 覆盖三类适配器和 MCP 返回；`TestInlineTokenQueueRecoveryAndRotation` 验证不持久化密钥、重启恢复与去重；配置文件非空 `token` 优先 | 本地通过 |
| Q12：短等待、后台任务、查询与取消 | 队列和取消测试；`TestHTTPTaskSurvivesClientDisconnect` 验证断开后后台完成并由新客户端取回 | 本地通过 |
| Q13：不盲目重试、不自动切换服务商、请求去重 | 并发去重和重启去重；断连、超时、5xx、429 测试；`TestMCPErrorResultsRemainStructured` 保留失败/未知任务信息 | 本地通过 |
| Q14：磁盘保存，默认不删除，可选保留期限 | `TestRetentionKeepsDeduplicationRecord`；过期后保留任务和去重记录，图片清理 | 本地通过 |
| Q16：实际网关与模型验收 | `TestLiveProviders/openai` 使用文件密钥通过，真实提交生成、参考编辑、蒙版各一次，核验内嵌预览与原图 | **所配置网关接口通过；蒙版效果限制见下文；OpenAI 官方与 Gemini 真实调用按用户选择不纳入当前验收** |
| Q18：SQLite 持久化与重启恢复 | `TestCrashRecoveryOverStdio` 强制终止实际子进程；已提交任务变未知，排队任务恢复，各请求仅提交一次 | 本地通过 |
| Q19：通用参数及受校验扩展，不静默忽略 | 严格 YAML、MCP 输入 schema、模型能力/枚举/扩展类型检查；`TestValidationBeforeSubmission`、`TestGeminiResolutionAndRatio` | 本地通过 |
| Q20：并发 2、队列 20、默认 1 张、超时 10 分钟且可配置 | 配置默认值与示例；队列满明确拒绝；配置驱动的超时、取消、去重测试 | 本地通过 |
| 启动与配置校验一致 | `TestHTTPCheckRejectsUnusableAddress`、`TestEmptyConfiguredPathsAreRejected`；CLI 配置检查无副作用，端口绑定失败前不启动任务线程 | 本地通过 |

## 可重复验证命令

```sh
go test -race ./... -count=1
go vet ./...
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
./bin/image-gen-mcp -config config.example.yaml -check
./bin/image-gen-mcp -config config.example.yaml -transport http -check
```

普通测试默认跳过 `TestLiveProviders`，不会调用真实图片服务。该跳过状态是未验收证据，不能当作真实服务通过。

## 真实服务验收：2026-09-17

配置文件直接提供 `models.openai.token`，无需密钥环境变量。实际地址为 `https://api.bitapi.codes/v1`，模型名 `gpt-image-2.5`；别名和适配器类型为 `openai`，不表示服务由 OpenAI 官方提供。

`TestLiveProviders/openai` 通过，总耗时约 120 秒。通过进程内 MCP 客户端提交生成、参考编辑、蒙版各一次，并轮询持久化任务；三个任务均为 `succeeded`，均返回 1254×1254 PNG 原图及 MCP 内嵌预览。此轮未指定尺寸、质量等可选参数，不能据此断言网关支持所有参数组合。stdio 和 HTTP 的进程/网络传输另由本地集成测试覆盖。

固定验收 ID：`bitapi-gpt-image-20260917`。原图位于 `data/live-verification/images/<task_id>/1.png`，预览位于同目录的 `1.png.preview.png`。

| 操作 | 任务 ID | 原图人工查看结果 |
| --- | --- | --- |
| 文生图 | `90271e62e673a9d1cc322f5206df02de` | 白底红方块，符合提示词 |
| 参考图编辑 | `6fc233e34c49e2e24de40077f469ea40` | 红方块变为蓝圆，白底保留，符合提示词 |
| 蒙版编辑 | `1a818e99073ef8971cdb00b8057e2747` | 中央生成小绿星，但蒙版外白底变为透明；接口通过，区域保留效果未通过 |

蒙版结果的 `(0,0)` 与 `(100,100)` 像素 alpha 均为 0，而原图同位置为完全不透明；界面上的黑底是透明区域的显示效果。请求分别使用 `image` 和 `mask` multipart 字段传递原图与蒙版。此结果不支持宣称该网关可以可靠保持蒙版外区域；服务原样保存上游结果，不自动合成或再次付费重试。

`edit_image` 工具说明也明确提示蒙版外区域可能改变、需查看结果，使调用 Agent 在工具发现阶段即可获知该限制。更新后已重新构建服务，并通过实际配置的 `-check` 校验。

文件密钥支持另有三类适配器的本地测试覆盖：非空 `token` 优先、密钥不写入 JSON 任务快照或工具结果、换密钥后恢复排队任务和去重。服务商、地址或模型发生变化时，旧排队任务拒绝套用新密钥。

## Gemini 零成本验收

使用 README 中的定向命令运行四组现有测试：`TestGeminiProtocol`、`TestGeminiResolutionAndRatio`、`TestEditWorkflowsThroughMCP/gemini`、`TestInlineTokenFromYAML/gemini`。测试使用本机 HTTP 模拟上游、临时 YAML 与测试密钥，无需真实 Gemini 配置，不产生生成费用。

2026-09-17 已执行上述命令，四组全部通过，开启 `-race` 未报告数据竞争；包测试耗时 2.350 秒。本轮未调用真实生成接口。

验证范围包括原生请求和鉴权格式、图片编码与响应解析、参数映射、文件密钥、MCP 生成与编辑结果、文件/URL/Base64 参考图以及不支持的蒙版在提交前被拒绝。生成质量、真实模型可用性与实际服务配额不由这些测试证明。

## 其他集成验证与可选真实验收

OpenAI 官方服务的本地协议测试已通过，官方服务未实测；真实 Gemini 未验证。按用户确认，两项真实服务验证均为可选，不阻塞当前交付。本次已在 Codex 中查看保存的原图；gbot 图片传递的具体问题与补丁见下文，pi 客户端尚未实测。

最新只读配置核验：`openai` 别名具备文件密钥且指向上述已测网关；`gateway` 的地址、模型仍为占位值且无密钥；`gemini` 的模型仍为占位值且无密钥，可保持未配置状态供将来接入。检查仅输出配置就绪状态，不输出密钥内容。

已从保存的项目列表定位本机 gbot：`/absolute/path/gbot`。其 SDK 原先将 MCP 图片转换为文字占位符。已按用户确认应用 [图片内容补丁](../patches/gbot-mcp-images.patch)，只修改三个 SDK 文件，运行配置保持原样。临时副本此前已通过模块竞态测试及实际 MCP 二进制的 stdio/HTTP 测试：五个工具可发现，生成结果包含可解码预览及可访问原图。测试只调用本机模拟上游。接入步骤见 [gbot 说明](gbot.md)。

补丁应用后，已在原始 gbot 的 `sdk/mcpclient` 模块执行全部测试并开启 `-race` 和实际图片 MCP 集成测试，全部通过（3.335 秒）。stdio 与 HTTP 都保留可解码预览，原图可读取或下载；`git diff --check` 通过。此次测试无真实 API 费用。

2026-09-18 增加实际 agentloop 循环验证：本机模拟模型发起生成工具调用，gbot 通过 stdio/HTTP 调用真实 MCP 程序，下一轮发往模型的 HTTP 请求中包含与返回预览一致的 `image_url` 内容块。两种传输均通过（3.162 秒），未调用真实视觉模型；测试证明图片进入模型输入，不把固定模拟回复当作模型理解图像的证据。

后续对现有网关复查时保留相同验收 ID、配置和数据目录，复用已有结果。新模型需提供对应本地配置；对蒙版效果进一步实验需明确选择新的请求 ID。`unknown` 不自动重试或切换服务商。

## 最终交付检查：2026-09-18

按用户确认的验收范围，功能实现与交付验证已完成：

- 图片 MCP 完整 `go test -race ./... -count=1` 通过：CLI 包 2.587 秒，核心包 19.698 秒；显式清除真实验收环境变量，本轮无付费调用。
- `go vet ./...` 通过；已构建二进制使用实际配置分别通过 stdio 与 HTTP 的 `-check`。
- gbot 补丁已应用，模块测试及两种传输的 Agent 循环图片转发测试通过；补丁文件同步包含最终测试。
- README、示例 YAML、本机 gbot 配置片段、接入说明及逐项证据已提供。

已知限制保留：OpenAI 官方与 Gemini 只有模拟验收；BitAPI 的蒙版结果未保留蒙版外背景，不保证模型编辑质量；pi 未单独实测。gbot 运行配置和已启动进程未自动更换，使用新代码需重新构建并按接入说明配置。以上边界不被表述为真实服务或所有客户端都已通过验收。
