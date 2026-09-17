# Launch checklist and copy

Suggested repository description:

> Generate and edit images through MCP. Go server for OpenAI, compatible gateways and Gemini, with previews, persistent tasks and request deduplication.

Suggested GitHub topics: `mcp`, `mcp-server`, `image-generation`, `image-editing`, `golang`, `openai`, `gemini`.

Before announcing, push the source, confirm CI passes, select a project license, and publish a tested release. Confirm `go install` works against the public repository. Use `docs/assets/demo-poster.png` as the repository social preview and `docs/assets/demo.gif` in posts where animation is supported. These are local replay demonstrations, not a recorded agent UI or a live generation benchmark.

## 中文发布文案草稿

做了一个 Go 写的图片 MCP 服务：在支持 MCP 的 Agent 里生成图片、用参考图编辑，并取回预览和原图。

支持 OpenAI Images、兼容网关和 Gemini 三类接口。比较关注长任务的处理：任务存进 SQLite，可以轮询、重启恢复；相同请求 ID 不重复提交，上游结果不确定时不自动重试。

仓库有无需密钥的本地演示，运行真实 MCP 子进程，回放之前验证过的图片。BitAPI 的文生图和参考编辑有真实验证；OpenAI 官方和 Gemini 目前通过模拟测试。欢迎试用并反馈你的客户端和模型兼容情况。

https://github.com/zouxiaoliang/image-gen-mcp

## English announcement draft

I built a Go MCP server for image generation and editing. It returns previews and original files, with adapters for OpenAI Images, compatible gateways, and Gemini.

It persists tasks in SQLite and deduplicates requests by ID. If an upstream result is uncertain, it records that state rather than automatically submitting another generation.

The repository includes a no-key demo using a real MCP subprocess and saved image fixtures. BitAPI generation and reference editing have been verified against the real service; OpenAI and Gemini currently have mock-based protocol coverage. Client and provider compatibility reports are welcome.

https://github.com/zouxiaoliang/image-gen-mcp

## After launch

Submit to relevant MCP directories and Awesome lists according to their contribution rules. Share a concrete example in communities where project posts are welcome. Over the next two weeks, track GitHub visitors, unique clones, installation failures, and actionable issues alongside stars. These drafts have not been posted anywhere.
