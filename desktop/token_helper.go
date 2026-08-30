package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/desktop/internal/configwriter"
	"github.com/Wei-Shaw/sub2api/desktop/internal/securestore"
)

const toolLauncherFlag = "--sub2api-launch-tool"

// runToolHelper handles the narrow non-GUI invocation emitted in the settings
// page. It starts the selected CLI itself, so the API key never crosses stdout
// or appears in a shell command. The caller's terminal remains attached to the
// child, which keeps interactive Codex/Claude sessions usable.
func runToolHelper(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != toolLauncherFlag {
		return false, 0
	}
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: --sub2api-launch-tool codex|claude")
		return true, 2
	}
	tool := strings.ToLower(strings.TrimSpace(args[1]))
	if tool != string(configwriter.ToolCodex) && tool != string(configwriter.ToolClaude) {
		fmt.Fprintln(os.Stderr, "unsupported local tool")
		return true, 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := appDataDir()
	writer, err := configwriter.NewJSONWriter(filepath.Join(root, "connection.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "desktop configuration unavailable")
		return true, 1
	}
	app := &App{config: writer, secrets: securestore.NewPlatformStore()}
	// startConfiguredTool performs the origin, key reference and executable
	// checks before constructing a direct exec.Cmd. It waits for the child so
	// the caller gets the CLI's normal terminal lifecycle and exit status.
	if _, err := app.startConfiguredToolWithContext(ctx, tool, true); err != nil {
		fmt.Fprintln(os.Stderr, sanitizeToolError(err))
		return true, 1
	}
	return true, 0
}

// startConfiguredToolWithContext is the helper-mode counterpart to the Wails
// binding. Keeping the context explicit avoids coupling a child process to a
// window lifecycle while preserving the same credential checks.
func (a *App) startConfiguredToolWithContext(ctx context.Context, tool string, wait bool) (ToolLaunchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// startConfiguredTool creates its own bounded context from appContext. For
	// the helper we temporarily use the supplied context as the app context;
	// no mutable state is shared with the GUI process.
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	return a.startConfiguredTool(tool, wait)
}

func sanitizeToolError(err error) error {
	if err == nil {
		return nil
	}
	// Errors produced by the launcher contain only tool/path metadata. Keep a
	// final guard here so a future lower-level error cannot accidentally echo a
	// credential returned by a platform provider.
	message := err.Error()
	for _, marker := range []string{"sk-", "key-", "token="} {
		if index := strings.Index(message, marker); index >= 0 {
			message = message[:index] + "[redacted]"
		}
	}
	return fmt.Errorf("%s", message)
}
