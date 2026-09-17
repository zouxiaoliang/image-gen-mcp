# Contributing

Provider compatibility reports, MCP client setup instructions, reproducible bug reports, and focused fixes are welcome.

Include the server version (`image-gen-mcp -version`), OS, transport, provider type, model name, and reproduction steps. Remove API keys, authorization headers, personal images, and private paths from configuration or logs.

Before submitting code, format Go changes with `gofmt` and run:

```sh
go test -race ./...
go vet ./...
go build -o bin/image-gen-mcp ./cmd/image-gen-mcp
go run ./examples/demo -server ./bin/image-gen-mcp
```

Default tests use mocks and must not require paid credentials. Report real provider verification separately; include the client version when contributing integration instructions.

中文：欢迎兼容性反馈、客户端接入说明、问题修复和文档改进。反馈中请附版本、系统、服务商与复现步骤，删除密钥及私人内容。普通测试无需付费账号，真实服务验证请单独标注。
