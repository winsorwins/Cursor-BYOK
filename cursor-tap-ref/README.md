# Cursor Proto Reference

这个目录不是完整的 `cursor-tap` 工具，而是从协议研究参考中保留下来的最小 proto 依赖包。`cursor-client` 通过 `go.mod replace` 引用它：

```go
replace github.com/burpheart/cursor-tap => ../cursor-tap-ref
```

## 为什么保留这个目录

Cursor 请求使用 Connect/gRPC-Web 和 protobuf 编码。`cursor-client` 需要 Go 结构体来解析和构造部分 Cursor 消息，例如：

- `agent.v1.AgentService/RunSSE`
- `aiserver.v1.BidiService/BidiAppend`
- `aiserver.v1.AiService/AvailableModels`
- Agent 工具调用和 checkpoint/KV blob 相关消息

这些结构体由 `cursor_proto/*.proto` 生成，位于 `cursor_proto/gen/`。

## 内容

- `cursor_proto/*.proto`: 协议定义
- `cursor_proto/gen/`: 已生成的 Go 包，普通开发和测试直接使用
- `cursor_proto/buf.yaml`: buf 配置
- `cursor_proto/buf.gen.yaml`: Go 代码生成配置
- `cursor_proto/buf.lock`: buf 锁文件

## 重新生成

如果修改了 proto，可以重新生成：

```bash
cd cursor_proto
buf generate
```

默认开源包已经包含 `cursor_proto/gen/`，因此 clone 后不需要先生成就能编译和测试。

## 来源和参考

协议研究参考自 `cursor-tap`：

https://github.com/burpheart/cursor-tap

本仓库已经移除了旧抓包工具、Web UI、数据样例和历史笔记，只保留当前主线客户端编译需要的 proto 参考部分。
