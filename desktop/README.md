# 神奇AI助手（桌面客户端骨架）

这是 Sub2API 的独立 Wails v2 客户端入口。它不启动或嵌入服务端，也不复用
`frontend/` 的 Web bundle；桌面前端和 Go module 都在本目录内维护。

## 发布平台

当前发布矩阵收敛为 **Windows x64（amd64）** 与 **macOS arm64（Apple Silicon）**。
两端均使用 Wails v2 原生壳和系统安全存储；Linux 可用于开发/测试，但暂不提供
官方安装包或发布承诺。

## 本地开发

```bash
cd desktop/frontend
pnpm install
pnpm run build

cd ..
go test ./...
```

安装 Wails CLI 后可在 `desktop/` 目录执行 `wails dev` 或 `wails build`；发布构建
应分别在 Windows amd64 与 macOS arm64 runner 上执行，避免跨平台 GUI/签名工具链
差异。

## 数据与密钥边界

- `connection.json` 只保存站点地址、gateway 地址、标签和密钥引用。
- API key、设备 access/refresh token 和 DPoP 私钥通过 `internal/securestore.Store`
  访问；生产默认使用 macOS Keychain、Windows Credential Manager 或 Linux Secret
  Service。仅设置 `SUB2API_DESKTOP_INSECURE_MEMORY_STORE=1` 时才启用进程内测试存储，
  这样 keyring 不可用时不会静默丢失或降级保存密钥。
- 当前默认实现已经使用 macOS Keychain、Windows Credential Manager 或 Linux
  Secret Service；界面显示为 `os` 保护等级。它不等同于 Secure Enclave/TPM
  硬件密钥，硬件绑定需要后续接入平台原生实现并重新验证。
- `protection_level` 是服务端归一化的声明：`hardware` 表示已验证的硬件绑定，
  `os` 表示系统凭证库，`software` 表示仅软件内存/密钥；客户端不会把 `os`
  级别冒充为 `hardware`。
- 系统凭证通常按当前 OS 用户和凭证库 ACL 保护。把安装包复制到另一台机器或另一个
  OS 用户通常不能继承这些条目，但同一 OS 用户下的恶意进程仍可能调用 helper 读取
  keyring；这不是硬件绑定或代码签名隔离。需要更强边界时，仍应接入 Secure Enclave/
  TPM、Keychain access group 和签名 helper，并在发布前单独验证。
- Codex/Claude Code 的一键配置会合并现有字段，写入前生成 `0600` 备份。Codex
  只更新 `config.toml` 的 provider 元数据，不修改 `auth.json`；桌面端提供受控
  启动辅助模式，从系统凭证库按需把 key 注入 Codex 子进程。Claude Code 的
  `settings.json` 仍由工具本身读取并包含认证字段，因此 UI 会在写入前明确提示
  同机进程/用户可读取该文件的风险。
- 生图结果由 `internal/imagestore.FileStore` 以 0600 文件和 SHA-256 元数据保存；
  异步任务 ID 会先写入 task checkpoint，再开始轮询。

## 连接模型

站点控制面（`<site>/api/v1`）与模型 gateway（公开设置中的 `api_base_url`）
分开保存。这样桌面 WebView 不会把 `/` 错误解析成本地 Wails origin，也不需要
为远程请求放宽浏览器 CORS。
