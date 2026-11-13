# Gemini Web 原生 CPA 插件

基于本仓库当前的网页协议、账号管理器和 API adapters 编译为原生共享库。插件 ID 为 `dadahhhh-gemini-web`，首版 `0.1.0`，目标宿主为支持动态插件的 **CLIProxyAPI v8.0.67**（C ABI v1，schema v10）。

## 支持范围

- 对话：OpenAI Chat Completions、Responses、Claude Messages、Gemini Native。
- 实时 SSE：使用内存管道逐块传递，保持原始字节和背压，CPA 关闭流后取消后端读取。
- Cookie / StorageState 导入、账号启停与删除、初始化重试、Cookie 轮换、账号并发与冷却调度均复用本仓库实现。
- 管理面板提供导入和状态查看；操作接口由 CPA 管理密钥保护，公开资源路由只提供静态页面。
- 默认仅路由 `gemini-web/` 前缀；没有导入账号时可手动启用访客会话。
- 不启动独立服务进程或监听端口，不需要 Python、Node 或浏览器运行时。

**首版不承诺完整覆盖独立服务的所有 HTTP 端点。** 文件、语音、视频任务轮询等独立端点没有注册进 CPA；对话请求内部的附件处理仍由上游 adapter 决定。未实现 CPA OAuth 一键登录或原生配额面板，也不把网页 Cookie 写进 CPA 的 Antigravity 认证文件。

## 构建

在仓库根目录执行：

```sh
cd plugin/cpa
go mod download
go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared -o dist/dadahhhh-gemini-web.so .
```

建议 Go 1.27.4 与 GCC。Linux ARM64、macOS 和 Windows 需在对应平台使用 C 编译器构建。此目录是独立 Go module，使用 `replace ../..` 引用本仓库，不给独立应用增加 CPA SDK 依赖。不要单独复制此目录后构建。

## 安装

先备份 CPA 配置与插件目录。把构建产物放入 `plugins/linux/amd64/dadahhhh-gemini-web.so`，然后合并配置：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    dadahhhh-gemini-web:
      enabled: true
      data_dir: /CLIProxyAPI/gemini-web-data
      proxy: http://your-proxy:7890
      guest_enabled: false
      model_prefix: gemini-web/
      per_account_concurrency: 1
      routing_strategy: round-robin
      request_timeout: 30m
```

Docker 需要把 `data_dir` 挂载到持久目录。该目录保存浏览器凭证与请求账本，应仅允许服务用户读写。相同目录只能由一个插件实例使用。`proxy` 是独立的网页端出口配置，按实际网络填写；不要直接套用 Antigravity 域名规则。

重启 CPA 后，从插件资源菜单打开“Gemini 网页账号”，或打开 `/v0/resource/plugins/dadahhhh-gemini-web/accounts`。在页面内输入 CPA **管理密钥**，再导入 Google Cookie 或 StorageState。密钥不存储到浏览器持久存储中。账号状态接口位于 `/v0/management/plugins/dadahhhh-gemini-web/status`。

初始化后查询插件管理接口 `/models` 获取账号发现的模型；CPA 注册时还会提供少量带前缀的启动目录。目录展示不代表该账号拥有对应模型权限。新发现模型可以通过前缀直接请求；重载插件会刷新 CPA 展示目录。

调用示例：

```sh
curl http://your-cpa:8317/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_CPA_API_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-web/gemini-3.8-flash","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

`gemini-3.8-flash-high` 等原有无前缀模型继续按 CPA 原配置处理。具体网页模型、权限与额度由登录账号决定。非流式执行受 `request_timeout` 限制；C ABI 不提供通用的即时客户端取消通知，非流式断开可能继续运行到后端结束或超时。

## 验证与发布

`go test ./...` 检查协议路由、前缀隔离、凭证边界、SSE 增量传递、取消释放、管理页面与无效导入。`tests/abi_smoke.py` 可对共享库运行真实 C ABI 加载、注册、调用和释放检查。GitHub Actions 会生成 Linux amd64 构建包。

自动测试不使用真实 Google Cookie。成功加载插件和通过本地测试，不等于已验证某个 Google 账号的付费模型、图片或工具能力。

## 致谢

网页协议、浏览器指纹、调度与 API 转换来自 Mag1cFall/Gemini-Web2API（MIT）。CPA C ABI 声明与接口来自 router-for-me/CLIProxyAPI（MIT）。本适配层由 dadahhhh fork 维护；不依赖其他第三方 Gemini Web CPA 插件。
