# Cursor BYOK Client

这是 Cursor BYOK 的当前主线实现：一个基于 Wails v2、Vue 3 和 Go 的本地桌面代理客户端。

## 模块职责

`cursor-client` 负责三件事：

1. 提供桌面 UI：模型配置、代理启停、请求日志、诊断信息和统计面板。
2. 启动本地代理：把 Cursor 的部分请求转接到本地 Relay。
3. 执行 BYOK Relay：把 Cursor 请求转换到用户自己的模型 API，再把流式响应转换回 Cursor 协议。

## 已实现功能

- 本地代理监听 `127.0.0.1:18080`
- 启动/停止代理并自动写入或恢复 Cursor 代理配置
- 本地 CA 生成、导出和 Windows 信任检测/安装
- Cursor Statsig/admin 启动缓存修复
- 模型配置增删改查和连通性测试
- `AvailableModels` 注入和 fallback
- OpenAI-compatible:
  - `/v1/responses`
  - `/v1/chat/completions`
- Anthropic-compatible:
  - `/v1/messages`
- Cursor Chat/Agent 请求解析和流式返回
- Agent/Ask/Plan 模式提示词和工具定义加载
- 本地工具执行：
  - `Read`
  - `Grep`
  - `Glob`
  - `Ls`
  - `Shell`
  - `ForceBackgroundShell`
  - `WriteShellStdin`
  - `PatchEdit`
  - `Write`
  - `Delete`
  - `ReadLints`
  - `WebFetch`
  - `TodoWrite`
  - `CreatePlan`
- SQLite 本地记录：
  - conversations
  - turns
  - messages
  - blobs
  - checkpoints
  - token details
  - tool calls
  - context snapshots
  - usage events

## 未实现或有限支持

- 非 Windows 平台的系统托盘和 CA 自动信任能力有限。
- MCP、WebSearch、Task、SwitchMode、AskQuestion、FetchMcpResource 等工具没有完整本地实现。
- 图片、多模态和代码补全链路没有完整打通。
- 当前没有独立 server mode、Docker 部署模式或自建后端服务。
- Cursor 协议变更后可能需要同步更新 `cursor-tap-ref/cursor_proto`。

## 开发环境

- Go 1.25+
- Node.js 18+
- Wails v2 CLI

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

## 开发运行

```bash
go mod tidy
cd frontend
npm install
npm run build
cd ..
wails dev
```

## 构建

```bash
cd frontend
npm install
npm run build
cd ..
wails build
```

构建产物会输出到 `build/bin/`，该目录不会提交到仓库。

## 目录结构

```text
cursor-client/
├── cmd/byok-tap/          # 本地调试/抓取辅助命令
├── frontend/              # Vue 前端
├── internal/
│   ├── bridge/            # Wails 后端服务桥接
│   ├── certs/             # CA 与证书管理
│   ├── config/            # 模型配置存储
│   ├── cursor/            # Cursor 设置处理
│   ├── database/          # SQLite 持久化
│   ├── mitm/              # 本地代理
│   ├── protocodec/        # Connect/gRPC 帧辅助处理
│   ├── relay/             # BYOK Relay 和 Agent 工具链路
│   ├── runtime/           # 运行路径
│   ├── statsig/           # Cursor 启动缓存辅助
│   └── tray/              # 系统托盘
├── build/                 # Wails 图标和平台元数据
├── go.mod
└── main.go
```

## 运行时数据

默认写入系统应用数据目录：

- Windows: `%APPDATA%/Cursor助手/`
- macOS: `~/Library/Application Support/Cursor助手/`

不要提交本地数据库、日志、证书或模型 API Key。
