package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/desktop/internal/configwriter"
)

// ToolLaunchResult is intentionally limited to process metadata.  In
// particular, it never returns the API key or a command line containing it.
type ToolLaunchResult struct {
	Tool                string `json:"tool"`
	Executable          string `json:"executable"`
	PID                 int    `json:"pid"`
	EnvironmentVariable string `json:"environment_variable"`
	StartedAt           string `json:"started_at"`
	Message             string `json:"message,omitempty"`
}

func newToolLaunchPlan(tool string) (ToolLaunchPlan, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	var envName string
	switch tool {
	case string(configwriter.ToolCodex):
		envName = "SUB2API_API_KEY"
	case string(configwriter.ToolClaude):
		envName = "ANTHROPIC_AUTH_TOKEN"
	default:
		return ToolLaunchPlan{}, configwriter.ErrUnsupportedTool
	}
	executable, err := os.Executable()
	if err != nil {
		return ToolLaunchPlan{}, fmt.Errorf("resolve desktop executable: %w", err)
	}
	if strings.TrimSpace(executable) == "" || strings.IndexFunc(executable, func(r rune) bool { return r == '\x00' || r == '\r' || r == '\n' }) >= 0 {
		return ToolLaunchPlan{}, errors.New("桌面可执行文件路径无效")
	}
	if runtime.GOOS == "windows" {
		quoted := quotePowerShell(executable)
		command := fmt.Sprintf("& %s --sub2api-launch-tool %s", quoted, tool)
		return ToolLaunchPlan{Tool: tool, EnvironmentVariable: envName, Command: command, Shell: "PowerShell", Note: "辅助模式直接启动 CLI；密钥不会经过 stdout、命令行参数或用户环境变量。"}, nil
	}
	quoted := quotePOSIX(executable)
	command := fmt.Sprintf("%s --sub2api-launch-tool %s", quoted, tool)
	return ToolLaunchPlan{Tool: tool, EnvironmentVariable: envName, Command: command, Shell: "POSIX shell", Note: "辅助模式直接启动 CLI；密钥不会经过 stdout、命令行参数或 shell 配置。"}, nil
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func toolEnvironmentName(tool string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case string(configwriter.ToolCodex):
		return "SUB2API_API_KEY", nil
	case string(configwriter.ToolClaude):
		return "ANTHROPIC_AUTH_TOKEN", nil
	default:
		return "", configwriter.ErrUnsupportedTool
	}
}

// LaunchTool starts an installed Codex or Claude Code process with the
// selected key injected into the child environment.  The renderer cannot
// provide an executable path or arbitrary arguments: both are resolved from a
// fixed tool name, which keeps shell/path injection out of this boundary.
func (a *App) LaunchTool(tool string) (ToolLaunchResult, error) {
	return a.startConfiguredTool(tool, false)
}

// startConfiguredTool is shared by the Wails action and the narrow command
// helper mode.  The helper uses wait=true so an interactive terminal keeps
// ownership of stdin/stdout; the Wails action starts asynchronously.
func (a *App) startConfiguredTool(tool string, wait bool) (ToolLaunchResult, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	envName, err := toolEnvironmentName(tool)
	if err != nil {
		return ToolLaunchResult{}, err
	}
	if a == nil || a.config == nil || a.secrets == nil {
		return ToolLaunchResult{}, errors.New("桌面连接或安全凭证存储不可用")
	}
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := contextWithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	config, err := a.config.Load(ctx)
	if err != nil {
		return ToolLaunchResult{}, err
	}
	if err := validateOfficialConfig(&config); err != nil {
		return ToolLaunchResult{}, err
	}
	key, err := a.toolAPIKey(ctx, config, tool)
	if err != nil {
		return ToolLaunchResult{}, fmt.Errorf("无法读取 %s 的 API key: %w", tool, err)
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.IndexFunc(key, func(r rune) bool { return r == '\x00' || r == '\r' || r == '\n' }) >= 0 {
		return ToolLaunchResult{}, errors.New("API key 不可用于启动本地客户端")
	}

	executable, err := findToolExecutable(tool)
	if err != nil {
		return ToolLaunchResult{}, err
	}
	command, err := commandForTool(executable)
	if err != nil {
		return ToolLaunchResult{}, err
	}
	command.Env = environmentWithSecret(os.Environ(), envName, key)
	// A Wails application normally has no terminal attached when opened from
	// Finder/Explorer. Inheriting the handles still makes the action useful when
	// the app was started from a terminal. The launch-plan command invokes the
	// helper from an existing terminal when interactive I/O is needed.
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	startedAt := time.Now().UTC().Format(time.RFC3339)
	if wait {
		if err := command.Start(); err != nil {
			clearCommandEnvironment(command, envName)
			key = ""
			return ToolLaunchResult{}, fmt.Errorf("启动 %s 失败: %w", tool, err)
		}
		pid := command.Process.Pid
		runErr := command.Wait()
		clearCommandEnvironment(command, envName)
		key = ""
		if runErr != nil {
			return ToolLaunchResult{}, fmt.Errorf("启动 %s 失败: %w", tool, runErr)
		}
		return ToolLaunchResult{Tool: tool, Executable: executable, PID: pid, EnvironmentVariable: envName, StartedAt: startedAt, Message: "已完成；API key 仅存在于该子进程环境。"}, nil
	}
	if err := command.Start(); err != nil {
		clearCommandEnvironment(command, envName)
		key = ""
		return ToolLaunchResult{}, fmt.Errorf("启动 %s 失败: %w", tool, err)
	}
	pid := command.Process.Pid
	// Wait in a detached goroutine so os/exec can release process resources and
	// so a Wails binding never blocks on the interactive CLI. The key is not
	// retained after Start; the child owns its environment copy.
	go func(cmd *exec.Cmd) { _ = cmd.Wait() }(command)
	clearCommandEnvironment(command, envName)
	key = ""
	return ToolLaunchResult{
		Tool: tool, Executable: executable, PID: pid, EnvironmentVariable: envName,
		StartedAt: startedAt,
		Message:   "已启动；API key 仅注入该子进程，退出后不会留在当前 shell。",
	}, nil
}

func clearCommandEnvironment(command *exec.Cmd, name string) {
	if command == nil {
		return
	}
	for i, entry := range command.Env {
		entryName := environmentEntryName(entry)
		if entryName != "" && strings.EqualFold(entryName, name) {
			command.Env[i] = name + "="
		}
	}
}

// contextWithTimeout is kept local to the launcher so tests can construct an
// App without a Wails startup context while still getting a bounded keyring
// lookup.
func contextWithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

func environmentWithSecret(base []string, name, value string) []string {
	result := make([]string, 0, len(base)+1)
	for _, entry := range base {
		// Windows environment names are case-insensitive. Removing case variants
		// on every platform keeps the child deterministic and avoids accidentally
		// retaining a stale credential under a differently cased name.
		if strings.EqualFold(environmentEntryName(entry), name) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, name+"="+value)
}

func environmentEntryName(entry string) string {
	index := strings.IndexByte(entry, '=')
	if index <= 0 {
		return ""
	}
	return entry[:index]
}

func findToolExecutable(tool string) (string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool != string(configwriter.ToolCodex) && tool != string(configwriter.ToolClaude) {
		return "", configwriter.ErrUnsupportedTool
	}
	// A GUI-launched process often receives a shorter PATH than an interactive
	// shell. Start with known absolute install locations, then inspect only
	// absolute PATH directories that pass the local permission/ACL checks. In
	// particular, do not call LookPath("codex") directly: an attacker that can
	// prepend a writable directory to PATH must not get the selected key injected
	// into an arbitrary executable. A same-user process can still invoke this
	// helper directly (the OS credential store is the actual account boundary),
	// but this avoids accidental cross-user PATH hijacking.
	candidates := toolExecutableCandidates(tool)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		path, err := exec.LookPath(candidate)
		if err == nil {
			if safePath, pathErr := validateToolPath(path); pathErr == nil {
				return safePath, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 %s 命令，请先安装后再启动", tool)
}

// toolExecutableCandidates returns absolute paths only. Keeping candidate
// construction separate from process execution makes the PATH trust policy
// testable without requiring a platform-specific CLI installation.
func toolExecutableCandidates(tool string) []string {
	candidates := make([]string, 0, 24)
	seen := make(map[string]struct{}, 24)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			return
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}
	home := ""
	if value, err := os.UserHomeDir(); err == nil && filepath.IsAbs(strings.TrimSpace(value)) {
		home = filepath.Clean(value)
	}
	if home == "" {
		value := strings.TrimSpace(os.Getenv("HOME"))
		if filepath.IsAbs(value) {
			home = filepath.Clean(value)
		}
	}

	switch runtime.GOOS {
	case "darwin":
		add(filepath.Join("/opt/homebrew/bin", tool))
		add(filepath.Join("/usr/local/bin", tool))
		if home != "" {
			for _, relative := range []string{
				filepath.Join(".local", "bin"),
				filepath.Join(".npm-global", "bin"),
				filepath.Join(".volta", "bin"),
				filepath.Join(".asdf", "shims"),
			} {
				add(filepath.Join(home, relative, tool))
			}
		}
	case "windows":
		userProfile := firstAbsoluteEnv("USERPROFILE", home)
		localAppData := firstAbsoluteEnv("LOCALAPPDATA", "")
		appData := firstAbsoluteEnv("APPDATA", "")
		programFiles := firstAbsoluteEnv("ProgramFiles", "")
		programFilesX86 := firstAbsoluteEnv("ProgramFiles(x86)", "")
		if localAppData != "" {
			add(filepath.Join(localAppData, "Programs", tool, tool+".exe"))
			add(filepath.Join(localAppData, "Programs", tool+".exe"))
		}
		if appData != "" {
			add(filepath.Join(appData, "npm", tool+".cmd"))
			add(filepath.Join(appData, "npm", tool+".bat"))
			add(filepath.Join(appData, "npm", tool+".exe"))
		}
		if userProfile != "" {
			for _, relative := range []string{
				filepath.Join(".local", "bin"),
				filepath.Join(".volta", "bin"),
			} {
				add(filepath.Join(userProfile, relative, tool+".exe"))
				add(filepath.Join(userProfile, relative, tool+".cmd"))
			}
		}
		for _, root := range []string{programFiles, programFilesX86} {
			if root == "" {
				continue
			}
			add(filepath.Join(root, tool, tool+".exe"))
			add(filepath.Join(root, tool, "bin", tool+".exe"))
		}
	default:
		add(filepath.Join("/usr/local/bin", tool))
		add(filepath.Join("/usr/bin", tool))
		if home != "" {
			for _, relative := range []string{
				filepath.Join(".local", "bin"),
				filepath.Join(".npm-global", "bin"),
				filepath.Join(".volta", "bin"),
				filepath.Join(".asdf", "shims"),
			} {
				add(filepath.Join(home, relative, tool))
			}
		}
	}

	// PATH remains useful for package managers with a user-selected prefix.
	// Only retain entries that are absolute, real directories and are not
	// writable by another Unix user (or that live below a standard per-user or
	// program-files root on Windows).
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if !trustedToolDirectory(directory) {
			continue
		}
		add(filepath.Join(directory, tool))
		if runtime.GOOS == "windows" {
			// LookPath applies PATHEXT, but explicit shims make the candidate list
			// deterministic and keep the policy easy to audit.
			add(filepath.Join(directory, tool+".exe"))
			add(filepath.Join(directory, tool+".cmd"))
			add(filepath.Join(directory, tool+".bat"))
		}
	}
	return candidates
}

func firstAbsoluteEnv(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if filepath.IsAbs(strings.TrimSpace(fallback)) {
		return filepath.Clean(strings.TrimSpace(fallback))
	}
	return ""
}

func trustedToolDirectory(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	linkInfo, err := os.Lstat(abs)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return trustedWindowsToolDirectory(abs)
	}
	// A directory with group/other write permission lets another local user
	// replace the command between resolution and Start. The executable itself
	// is checked again by validateToolPath.
	return linkInfo.Mode().Perm()&0o022 == 0
}

func trustedWindowsToolDirectory(path string) bool {
	home, _ := os.UserHomeDir()
	roots := []string{
		firstAbsoluteEnv("USERPROFILE", home),
		firstAbsoluteEnv("LOCALAPPDATA", ""),
		firstAbsoluteEnv("APPDATA", ""),
		firstAbsoluteEnv("ProgramFiles", ""),
		firstAbsoluteEnv("ProgramFiles(x86)", ""),
	}
	for _, root := range roots {
		if pathWithinDirectory(root, path) {
			return true
		}
	}
	return false
}

func pathWithinDirectory(root, path string) bool {
	root = strings.TrimSpace(root)
	path = strings.TrimSpace(path)
	if root == "" || path == "" || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		path = strings.ToLower(path)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return false
	}
	return true
}

func validateToolPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("本地客户端路径为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(abs) {
		return "", errors.New("本地客户端路径必须是绝对路径")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if linkInfo, linkErr := os.Lstat(abs); linkErr != nil {
		return "", linkErr
	} else if linkInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("本地客户端不能是符号链接")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("本地客户端不是普通可执行文件")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("本地客户端不可执行")
	}
	// A group/world-writable directory would let another local user replace the
	// binary between LookPath and Start. Reject it on Unix; Windows ACLs are not
	// represented reliably by Go's mode bits and are left to the OS.
	if runtime.GOOS != "windows" {
		if dirInfo, dirErr := os.Stat(filepath.Dir(abs)); dirErr == nil && dirInfo.Mode().Perm()&0o022 != 0 {
			return "", errors.New("本地客户端所在目录可被其他用户写入")
		}
	}
	return abs, nil
}

func commandForTool(executable string) (*exec.Cmd, error) {
	if strings.TrimSpace(executable) == "" || strings.IndexFunc(executable, func(r rune) bool { return r == '\x00' || r == '\r' || r == '\n' }) >= 0 {
		return nil, errors.New("本地客户端路径无效")
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(executable))
		if ext == ".cmd" || ext == ".bat" {
			// Batch shims are common for npm-installed CLIs.  The path is resolved
			// by LookPath and quoted as one fixed command; no renderer arguments or
			// shell fragments are accepted.
			if unsafeWindowsShimPath(executable) {
				return nil, errors.New("拒绝包含特殊字符的 Windows 客户端路径")
			}
			return exec.Command("cmd.exe", "/d", "/s", "/c", `call "`+executable+`"`), nil
		}
	}
	return exec.Command(executable), nil
}

// unsafeWindowsShimPath deliberately allows both Windows path separators.
// Backslashes are data inside the quoted cmd.exe argument; only characters
// that can terminate/expand a command are rejected here. The path is still
// required to be an existing, non-symlink executable by validateToolPath.
func unsafeWindowsShimPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return true
	}
	if strings.IndexFunc(path, func(r rune) bool { return r == '\x00' || r == '\r' || r == '\n' }) >= 0 {
		return true
	}
	return strings.ContainsAny(path, "\"%&|<>^!()")
}
