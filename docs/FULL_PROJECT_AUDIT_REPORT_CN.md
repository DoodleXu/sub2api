# Sub2API 全项目审核与修复报告

## 1. 结论

| 项目 | 结果 |
| --- | --- |
| 审核对象 | 完整仓库 HEAD 及本轮全部未提交修复 |
| 审核执行 HEAD | `e48037003b738f93d7ecf6630f820077de1fe645` |
| 分支 / 版本 | `main` / `0.1.232` |
| 审核时间 | 2026-07-29（Asia/Shanghai） |
| P0 | 0 |
| P1 | 1 / 已关闭 1 |
| P2 | 2 / 已关闭 2 |
| P3 | 0 |
| 工程验收 | 通过 |
| 最终结论 | 有条件通过：代码和自动门禁已闭环，正式提交或发版前仍需独立复核人签字 |

本报告承接并替代一次性审核计划 `docs/FULL_PROJECT_AUDIT_PLAN_CN.md`。审核没有连接或修改生产资源；容器启动验收仅使用带 `sub2api-audit-20260729-*` 前缀的隔离资源，验收后已全部删除。

## 2. 发现项

### [P1] AUD-FE-001：pnpm 9 frozen install 与锁文件配置不一致

- 状态：已关闭。
- 模块：前端依赖与 CI/Release。
- 文件：`frontend/package.json`、`frontend/pnpm-workspace.yaml`、`frontend/pnpm-lock.yaml`。
- 触发条件：使用 CI 固定的 pnpm `9.15.9` 执行 `pnpm install --frozen-lockfile`。
- 修复前行为：报 `ERR_PNPM_LOCKFILE_CONFIG_MISMATCH`，阻断 CI、Security Scan、Docker 和 Release 前端任务。
- 根因：新增安全 overrides 只写入 workspace 配置，没有同步仓库已有的 `package.json#pnpm.overrides` 兼容入口。
- 修复：两处 overrides 保持一致；使用 pnpm `9.15.9` 重建锁文件。
- 复验：pnpm 9 frozen install 成功，安装前后锁文件 SHA256 均为 `77bb8f9abbf2b20632df3d0caf4fc216ecd3ef9ded878fa9afe4d7fd264e87f4`。

### [P2] AUD-DOC-001：完整审核缺少正式、可提交的证据载体

- 状态：已关闭。
- 模块：审核治理与文档。
- 修复前行为：一次性计划中的基线、发现项、证据和结论为空，并且计划及拟建报告均被 `docs/*` 忽略。
- 根因：审核过程完成后没有转存正式报告，也没有为报告建立 Git 白名单。
- 修复：新增本报告并在 `.gitignore` 明确放行；删除一次性计划；报告记录基线、发现项、复验和独立复核边界。
- 复验：`git check-ignore` 不再忽略本报告，报告会进入最终差异。

### [P2] AUD-IMG-001：Apple Silicon 构建 amd64 镜像时前端 builder 经 QEMU 崩溃

- 状态：已关闭。
- 模块：Docker 多架构构建。
- 文件：`deploy/Dockerfile`。
- 触发条件：在 arm64 Docker Desktop 执行 `docker buildx build --platform linux/amd64`。
- 修复前行为：amd64 Node builder 中的 esbuild 在 QEMU 下触发 Go runtime `lfstack.push invalid packing` 并中止构建。
- 根因：所有多阶段镜像都隐式使用目标平台，构建工具也被迫在模拟架构运行。
- 修复：Node 和 Go builder 固定使用 `$BUILDPLATFORM`；Go 使用 `$TARGETOS/$TARGETARCH` 交叉编译；最终运行层保持目标平台。
- 复验：`linux/amd64` 镜像构建成功且无 BuildKit 警告；镜像检查为 `os=linux arch=amd64`，内置服务可启动并报告版本 `0.1.232`。

## 3. 修复范围

- 支付管理写操作统一接入 step-up 2FA，公开支付恢复接口接入面板 IP 限流，并补充路由安全测试。
- Release 固定已验证 tag，校验 tag 格式、默认分支祖先关系及精确提交的 CI/Security Scan 成功状态。
- 未实现统计接口由伪造成功数据改为明确的 `501 / STATS_NOT_IMPLEMENTED`，前端移除对应未使用客户端。
- 用量导出由存在高危漏洞的 `xlsx` 改为带公式注入防护的 UTF-8 CSV。
- 清理安全例外并加强例外校验器；DOMPurify、Mermaid、UUID、YAML、PostCSS 使用已修复版本。
- 消除 Gin 全局模式的测试竞态，恢复账号批量负载集成断言，并将异步测试计数改为原子操作。
- 账号管理大型弹窗改为按需加载，修复组件卸载后的定时任务生命周期。
- 补充 standalone 外部数据库/Redis 示例，统一迁移文档的 forward-only 恢复语义。
- 修复 pnpm 9 锁文件兼容和 Docker 跨平台 builder。

## 4. 验证证据

### 后端

| 门禁 | 结果 |
| --- | --- |
| `go mod verify` | 通过，所有模块校验成功 |
| `TZ=UTC go test -tags=unit -count=1 ./...` | 通过 |
| `TZ=UTC go test -tags=integration -count=1 ./...` | 通过，Docker/testcontainers 实际执行 |
| `TestConcurrencyCacheSuite/TestGetAccountsLoadBatch` | 通过，PostgreSQL 18.1 + Redis 8.4 |
| `TZ=UTC go vet -tags=unit ./...` | 通过 |
| `golangci-lint v2.9.0 run --timeout=30m ./...` | `0 issues` |
| 五个高风险包 `go test -race -tags=unit` | 通过 |
| `go build -trimpath ./cmd/server` | 通过 |
| `govulncheck ./...` | 实际调用路径 0 个漏洞 |
| `go test ./migrations -count=1` | 通过 |

### 前端

| 门禁 | 结果 |
| --- | --- |
| pnpm `9.15.9` frozen install | 通过且锁文件哈希不变 |
| `pnpm run test:run` | 210 个测试文件、1452 个用例全部通过 |
| `pnpm run typecheck` | 通过 |
| `pnpm run lint:check` | 通过 |
| `pnpm run build` | 通过，973 个模块；AccountsView 主 chunk 约 158 kB |
| 生产依赖审计 | info/low/moderate/high/critical 全部为 0 |
| 审计例外校验 | 通过，当前例外为空 |

### 部署、容器与发布

| 门禁 | 结果 |
| --- | --- |
| `deploy/test-caddyfile-cache.sh` | 通过 |
| 四个 Compose 变体 `config --quiet` | 全部通过 |
| `actionlint` | 通过 |
| OpenSpec `validate --all --strict` | 1 passed，0 failed |
| `docker buildx build --platform linux/amd64 --load` | 通过，无警告 |
| 镜像架构 | `linux/amd64` |
| 隔离空库启动 | 通过，自动迁移后 public schema 93 张表 |
| 健康检查与重启 | 首次启动、重启后均返回 `{"status":"ok"}` |
| 隔离资源清理 | 3 个容器、3 个卷、1 个网络全部删除 |

## 5. 剩余边界

- 未执行真实支付、真实 OAuth、真实上游模型、生产数据库恢复或生产部署；这些操作不在本次授权范围内。
- 没有创建提交、推送、tag、GitHub Release 或部署。
- 独立第二审核人尚未签字。本报告不把同一执行者的复审冒充独立双人复核；提交或发版负责人应重点复核 P1、支付权限矩阵、Release gate、迁移和 Docker 跨平台修改。

## 6. 最终批准

| 角色 | 状态 |
| --- | --- |
| 主审核与修复 | Codex，2026-07-29 |
| 独立复核人 | 待填写 |
| 风险接受 | 无未处置 P0/P1；无需风险接受 |
