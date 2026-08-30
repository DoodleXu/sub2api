package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/desktop/internal/configwriter"
	"github.com/Wei-Shaw/sub2api/desktop/internal/securestore"
	"github.com/Wei-Shaw/sub2api/desktop/internal/siteclient"
)

func TestEnvironmentWithSecretReplacesParentValue(t *testing.T) {
	env := environmentWithSecret([]string{"PATH=/bin", "SUB2API_API_KEY=old", "sub2api_api_key=case-variant", "OTHER=keep"}, "SUB2API_API_KEY", "new")
	joined := strings.Join(env, "\n")
	if strings.Count(joined, "SUB2API_API_KEY=") != 1 || strings.Contains(joined, "sub2api_api_key=") || !strings.Contains(joined, "SUB2API_API_KEY=new") || strings.Contains(joined, "old") || strings.Contains(joined, "case-variant") {
		t.Fatalf("unexpected child environment: %v", env)
	}
}

func TestToolLaunchPlanUsesHelperWithoutSecret(t *testing.T) {
	plan, err := newToolLaunchPlan(string(configwriter.ToolCodex))
	if err != nil {
		t.Fatal(err)
	}
	if plan.EnvironmentVariable != "SUB2API_API_KEY" || !strings.Contains(plan.Command, toolLauncherFlag) {
		t.Fatalf("unexpected launch plan: %+v", plan)
	}
	for _, secret := range []string{"sk-test", "OPENAI_API_KEY", "SUB2API_API_KEY="} {
		if strings.Contains(plan.Command, secret) {
			t.Fatalf("launch plan contains credential material %q: %s", secret, plan.Command)
		}
	}
	if plan.Shell == "" || plan.Note == "" {
		t.Fatalf("launch plan is missing operator guidance: %+v", plan)
	}
}

func TestToolLaunchPlanRejectsUnsupportedTool(t *testing.T) {
	if _, err := newToolLaunchPlan("unknown"); !errors.Is(err, configwriter.ErrUnsupportedTool) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunToolHelperRejectsExtraArguments(t *testing.T) {
	handled, exitCode := runToolHelper([]string{toolLauncherFlag, string(configwriter.ToolCodex), "--unexpected"})
	if !handled || exitCode != 2 {
		t.Fatalf("helper accepted extra arguments: handled=%v exitCode=%d", handled, exitCode)
	}
}

func TestRunToolHelperRejectsUnknownTool(t *testing.T) {
	handled, exitCode := runToolHelper([]string{toolLauncherFlag, "shell"})
	if !handled || exitCode != 2 {
		t.Fatalf("helper accepted unknown tool: handled=%v exitCode=%d", handled, exitCode)
	}
}

func TestWindowsShimPathSafetyAllowsSeparators(t *testing.T) {
	for _, path := range []string{
		`C:\Users\Alice\AppData\Roaming\npm\codex.cmd`,
		`C:\Program Files\Codex\codex.bat`,
		`C:/Users/Alice/bin/claude.cmd`,
	} {
		if unsafeWindowsShimPath(path) {
			t.Fatalf("普通 Windows 路径被错误拒绝: %q", path)
		}
	}
	for _, path := range []string{
		`C:\Users\Alice\bin\codex&debug.cmd`,
		`C:\Users\Alice\bin\codex|debug.cmd`,
		`C:\Users\Alice\bin\codex%PATH%.cmd`,
		"C:\\Users\\Alice\\bin\\codex\n.cmd",
		`C:\Users\Alice\bin\codex".cmd`,
	} {
		if !unsafeWindowsShimPath(path) {
			t.Fatalf("Windows shell 元字符路径未被拒绝: %q", path)
		}
	}
}

func TestToolExecutableCandidatesUseAbsoluteTrustedEntries(t *testing.T) {
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", "."+string(os.PathListSeparator)+oldPath)
	candidates := toolExecutableCandidates(string(configwriter.ToolCodex))
	if len(candidates) == 0 {
		t.Fatal("expected known absolute candidates")
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			t.Fatalf("candidate is not absolute: %q", candidate)
		}
		if candidate == string(configwriter.ToolCodex) {
			t.Fatalf("bare tool candidate bypasses PATH trust policy: %q", candidate)
		}
	}
}

func TestTrustedToolDirectoryRejectsRelativeAndWritableEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission policy")
	}
	if trustedToolDirectory(".") {
		t.Fatal("relative PATH directory was accepted")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if trustedToolDirectory(dir) {
		t.Fatal("writable PATH directory was accepted")
	}
}

func TestLaunchToolInjectsOnlyChildEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "child-env.txt")
	toolPath := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nprintf '%s' \"$SUB2API_API_KEY\" > \"$SUB2API_TEST_OUTPUT\"\n"
	if err := os.WriteFile(toolPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SUB2API_API_KEY", "parent-value")
	t.Setenv("SUB2API_TEST_OUTPUT", output)
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(context.Background(), apiKeyRef, "child-value"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config: &appTestConfigWriter{config: configwriter.ConnectionConfig{
			SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
			AuthMode: "api_key", APIKeyRef: apiKeyRef,
		}},
		secrets: secrets,
	}
	result, err := app.LaunchTool(string(configwriter.ToolCodex))
	if err != nil {
		t.Fatal(err)
	}
	if result.PID <= 0 || result.EnvironmentVariable != "SUB2API_API_KEY" {
		t.Fatalf("unexpected launch result: %+v", result)
	}
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(output)
		if string(data) == "child-value" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(data) != "child-value" {
		t.Fatalf("child did not receive selected key: %q", data)
	}
	if got := os.Getenv("SUB2API_API_KEY"); got != "parent-value" {
		t.Fatalf("parent environment was changed: %q", got)
	}
}

func TestCommandForToolRejectsUnsafePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific command quoting")
	}
	if _, err := commandForTool(`C:\Program Files\bad&tool.cmd`); err == nil {
		t.Fatal("unsafe batch path was accepted")
	}
}

func TestValidateToolPathRejectsWritableDirectoryAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink checks")
	}
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "codex")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := validateToolPath(toolPath); err == nil {
		t.Fatal("group-writable tool directory was accepted")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "codex-link")
	if err := os.Symlink(toolPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := validateToolPath(linkPath); err == nil {
		t.Fatal("tool symlink was accepted")
	}
}
