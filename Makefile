.PHONY: build build-backend build-frontend build-desktop test test-backend test-frontend test-frontend-critical test-desktop desktop-frontend

FRONTEND_CRITICAL_VITEST := \
	src/api/__tests__/client.spec.ts \
	src/api/__tests__/tokenRefresh.spec.ts \
	src/api/__tests__/channelMonitorV2.spec.ts \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/views/user/__tests__/ChannelStatusView.mode.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts \
	src/features/channel-monitor-v2/__tests__/designSystem.structure.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorFormat.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorZoom.spec.ts

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# Build the Wails client independently from the server's Vue bundle. The
# Wails CLI is intentionally not required for the ordinary repository build;
# native packaging is performed by .github/workflows/desktop.yml on the
# platform runners.
desktop-frontend:
	@pnpm --dir desktop/frontend install --frozen-lockfile
	@pnpm --dir desktop/frontend run build

build-desktop: desktop-frontend
	@cd desktop && GOCACHE=$${GOCACHE:-/tmp/sub2api-desktop-cache} go build ./...

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@$(MAKE) test-frontend-critical

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST)

test-desktop:
	@pnpm --dir desktop/frontend install --frozen-lockfile
	@pnpm --dir desktop/frontend run typecheck
	@cd desktop && GOCACHE=$${GOCACHE:-/tmp/sub2api-desktop-cache} go test ./...
