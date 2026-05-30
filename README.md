# Cursor BYOK

## 维护状态

本项目后续不再更新维护，代码仅作为学习、研究和实现参考。由于 Cursor 协议、客户端行为和第三方模型 API 都可能随时变化，本仓库不保证代码可持续可用，也不提供问题修复、功能适配或使用支持。使用过程中遇到的问题请自行排查和处理，相关风险由使用者自行承担。

Cursor BYOK 是一个本地桌面代理实验项目。它尝试把 Cursor 客户端的部分 AI 请求转接到用户自己配置的模型 API 上，让开发者可以在本地使用 OpenAI-compatible 或 Anthropic-compatible 的模型服务。

本项目不是 Cursor 官方项目，也不是完整的 Cursor 后端替代品。它主要用于协议研究、本地代理实验和 BYOK（Bring Your Own Key）工作流验证。请在遵守 Cursor 服务条款、第三方模型服务条款、软件许可和当地法律法规的前提下使用。

## 这个项目实现了什么

当前开源版本只保留“目前能实现功能的主线客户端”，包含：

- 桌面客户端：基于 Wails v2、Vue 3 和 Go，提供代理控制、模型配置、诊断信息和请求统计界面。
- 本地 MITM 代理：默认监听 `127.0.0.1:18080`，通过本地 CA 证书解密和转发 Cursor API 流量。
- Cursor 代理设置管理：启动代理时写入 Cursor 网络代理配置，停止或修复时可恢复原配置。
- 本地 CA 管理：生成 CA 证书，Windows 下支持安装/检测受信任状态。
- 模型目录注入：拦截或补齐 Cursor `AvailableModels` 响应，把本地配置的 BYOK 模型暴露给 Cursor 模型选择器。
- BYOK 文本对话转发：将 Cursor 的 Chat/Agent 请求转换为 OpenAI-compatible 或 Anthropic-compatible 请求，并把流式响应转换回 Cursor 可消费的格式。
- Agent/Ask/Plan 模式提示词和工具定义加载：从 `cursor提示词与工具调用/` 读取模式提示词和工具 schema。
- 部分本地 Agent 工具执行：支持文件读取、搜索、目录列举、Shell、文件写入、删除、PatchEdit、TodoWrite、CreatePlan、WebFetch 等本地能力。
- 会话和统计记录：使用 SQLite 记录会话、turn、消息、token、工具调用、checkpoint 和本地 KV blob。
- 运行状态诊断：提供代理状态、CA 状态、Cursor 代理状态、请求日志、Token/费用估算等诊断信息。

## 暂未实现或不完整的功能

为了避免误解，下面这些能力当前没有完整实现：

- 不提供完整 Cursor 后端，不实现账号、订阅、鉴权、云同步、团队功能或官方服务替代。
- 不承诺绕过任何付费、访问控制或平台限制。
- 当前主线是桌面客户端，没有保留旧版独立后端、Docker 服务端或旧版管理端。
- 主要测试目标是 Windows；非 Windows 的 CA 信任安装和系统托盘能力较有限。
- 只支持 OpenAI-compatible 和 Anthropic-compatible 的流式文本接口；其他 provider 需要新增 adapter。
- 图片、多模态、代码补全和所有 Cursor 专有 RPC 没有完整打通。
- MCP、WebSearch、Task、SwitchMode、AskQuestion 等 Cursor UI/云端相关工具目前返回不可用或只做有限兼容。
- 压缩的部分请求帧、复杂协议分支和未来 Cursor 协议变更可能无法解析。
- 没有本地向量索引、语义搜索、长期记忆或多人协作功能。

## 工作原理

整体链路如下：

```text
Cursor IDE
   |
   |  HTTP / HTTPS / Connect / gRPC-Web
   v
127.0.0.1:18080 本地代理
   |
   +-- 非目标请求：按原样转发到官方或原始上游
   |
   +-- AvailableModels：注入本地 BYOK 模型
   |
   +-- BYOK 模型请求：
          1. 解码 Cursor 请求体和流式帧
          2. 解析模型名、消息、模式、上下文和工具调用
          3. 根据模型配置转换为 OpenAI / Anthropic 请求
          4. 请求用户自己的模型 API
          5. 把 provider 的 SSE 文本流转换回 Cursor 需要的帧
          6. 执行本地工具并记录会话、Token 和工具结果
```

关键模块：

- `cursor-client/internal/mitm`: 基于 `goproxy` 的 HTTP/HTTPS MITM 代理。
- `cursor-client/internal/certs`: 本地 CA、动态站点证书和系统信任管理。
- `cursor-client/internal/cursor`: Cursor 配置、代理设置和启动缓存修复。
- `cursor-client/internal/relay`: 请求路由、模型目录注入、BYOK 转发、Agent 工具循环。
- `cursor-client/internal/database`: SQLite 持久化 schema 和操作。
- `cursor-tap-ref/cursor_proto`: Cursor 相关 proto 定义和生成后的 Go 代码。
- `cursor提示词与工具调用`: Agent/Ask/Plan 模式提示词与工具 schema。

## 目录结构

```text
.
├── cursor-client/              # 当前主线桌面客户端
├── cursor-tap-ref/             # Proto 参考依赖，只保留当前编译需要的部分
├── cursor提示词与工具调用/       # 模式提示词和工具定义
├── README.md
├── CONTRIBUTING.md
├── SECURITY.md
├── LICENSE
└── go.work
```

## 快速开始

### 环境要求

- Go 1.25+
- Node.js 18+
- Wails v2 CLI

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

### 开发运行

```bash
cd cursor-client
go mod tidy
cd frontend
npm install
npm run build
cd ..
wails dev
```

### 构建

```bash
cd cursor-client
cd frontend
npm install
npm run build
cd ..
wails build
```

构建产物在 `cursor-client/build/bin/`，不会提交到仓库。

## 使用流程

1. 启动桌面客户端。
2. 在模型配置页添加模型：
   - Provider: `OpenAI` 或 `Anthropic`
   - Base URL: 你的模型服务地址
   - API Key: 你的模型服务密钥
   - Model ID: provider 真实模型名
3. 在仪表盘启动本地代理。
4. 按提示信任本地 CA 证书。
5. 打开 Cursor，在模型列表中选择注入后的 BYOK 模型。

## 参考项目和主要依赖

| 项目 | 地址 | 用途 |
| --- | --- | --- |
| cursor-tap | https://github.com/burpheart/cursor-tap | Proto 定义和 Cursor 协议研究参考；本仓库只保留当前编译需要的 proto/gen 部分 |
| cursor-byok | https://github.com/leookun/cursor-byok | cursor自定义模型使用 |
| Wails | https://github.com/wailsapp/wails | Go + WebView 桌面应用框架 |
| goproxy | https://github.com/elazarl/goproxy | HTTP/HTTPS MITM 代理基础能力 |
| systray | https://github.com/getlantern/systray | Windows 系统托盘 |
| modernc.org/sqlite | https://gitlab.com/cznic/sqlite | 纯 Go SQLite 驱动 |
| Protocol Buffers Go | https://github.com/protocolbuffers/protobuf-go | protobuf 编解码 |
| Vue | https://github.com/vuejs/core | 前端框架 |
| Vite | https://github.com/vitejs/vite | 前端构建工具 |
| Pinia | https://github.com/vuejs/pinia | 前端状态管理 |
| Lucide | https://github.com/lucide-icons/lucide | 前端图标 |

## 开源导出说明

这个目录是从开发工作区筛选出的开源版本，已排除：

- `node_modules/`、前端 `dist/`、`.next/`、`coverage/`
- 可执行文件、测试二进制和本地构建产物
- 运行日志、数据库、证书、私钥和 `.env`
- 个人草稿、阶段报告、逆向字符串 dump 和疑似敏感样例
- 早期后端原型、旧版管理端和旧版抓包工具 UI
- 嵌套仓库的 `.git/` 目录

## 发布前检查

```bash
rg -n -S "(API_KEY|SECRET|PASSWORD|TOKEN|PRIVATE KEY|sk-[A-Za-z0-9])" .

cd cursor-client
go test ./...

cd ../cursor-tap-ref
go test ./...
```

## 许可证

MIT License，见 `LICENSE`。
