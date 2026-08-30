package configwriter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func TestIntegrateClaudePreservesSettingsAndBacksUp(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "{\"permissions\":{\"allow\":[\"Read\"]},\"env\":{\"CUSTOM\":\"keep\"}}\n"
	if err := os.WriteFile(settingsPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := IntegrateTool(context.Background(), ToolIntegrationInput{Tool: ToolClaude, HomeDir: home, BaseURL: "https://ai.clol.site/antigravity", APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || !result.Files[0].Changed || result.Files[0].BackupPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["permissions"]; !ok {
		t.Fatalf("unrelated settings were lost: %s", data)
	}
	env := doc["env"].(map[string]any)
	if env["CUSTOM"] != "keep" || env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
		t.Fatalf("env merge failed: %#v", env)
	}
	backup, err := os.ReadFile(result.Files[0].BackupPath)
	if err != nil || string(backup) != old {
		t.Fatalf("backup mismatch: %v %q", err, backup)
	}
	if _, err := RestoreToolFile(context.Background(), result.Files[0].BackupPath, settingsPath); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(settingsPath)
	if string(restored) != old {
		t.Fatalf("restore mismatch: %q", restored)
	}
}

func TestIntegrateCodexPreservesConfigAndMergesAuth(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".codex", "config.toml")
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	oldConfig := "model = \"custom-model\"\n[features]\ngoals = true\n[model_providers.sub2api]\nexperimental_bearer_token = \"old\"\n"
	if err := os.WriteFile(configPath, []byte(oldConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{\"OTHER\":\"keep\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := IntegrateTool(context.Background(), ToolIntegrationInput{Tool: ToolCodex, HomeDir: home, BaseURL: "https://ai.clol.site", APIKey: "sk-test", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].BackupPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	configData, _ := os.ReadFile(configPath)
	var config map[string]any
	if err := toml.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config["model"] != "custom-model" || config["model_provider"] != "sub2api" {
		t.Fatalf("model/config merge failed: %#v", config)
	}
	providers, ok := config["model_providers"].(map[string]any)
	if !ok {
		t.Fatalf("missing providers: %#v", config)
	}
	provider, ok := providers["sub2api"].(map[string]any)
	if !ok || provider["base_url"] != "https://ai.clol.site/v1" || provider["env_key"] != "SUB2API_API_KEY" {
		t.Fatalf("provider merge failed: %#v", providers)
	}
	if _, exists := provider["experimental_bearer_token"]; exists {
		t.Fatalf("legacy bearer token must be removed: %#v", provider)
	}
	authData, _ := os.ReadFile(authPath)
	if string(authData) != "{\"OTHER\":\"keep\"}\n" {
		t.Fatalf("Codex auth.json was modified: %s", authData)
	}
	if strings.Contains(string(configData), "sk-test") || strings.Contains(string(authData), "sk-test") {
		t.Fatal("Codex API key was written to a local configuration file")
	}
}

func TestIntegrateToolRejectsMalformedConfigWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := IntegrateTool(context.Background(), ToolIntegrationInput{Tool: ToolClaude, HomeDir: home, BaseURL: "https://ai.clol.site", APIKey: "sk-test"}); err == nil {
		t.Fatal("expected malformed config error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "not-json" {
		t.Fatalf("malformed file was overwritten: %q", data)
	}
}

func TestIntegrateClaudeAllowsFirstInstallWithoutBackup(t *testing.T) {
	home := t.TempDir()
	result, err := IntegrateTool(context.Background(), ToolIntegrationInput{
		Tool: ToolClaude, HomeDir: home, BaseURL: "https://ai.clol.site", APIKey: "sk-first-install",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || !result.Files[0].Changed || result.Files[0].BackupPath != "" {
		t.Fatalf("unexpected first-install result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
}

func TestBackupToolFileUsesDistinctRecoverableNames(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"keep\":true}\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := BackupToolFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BackupToolFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if first.BackupPath == second.BackupPath {
		t.Fatalf("backup names collided: %q", first.BackupPath)
	}
	for _, backup := range []ToolBackup{first, second} {
		data, readErr := os.ReadFile(backup.BackupPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != string(content) {
			t.Fatalf("backup %s changed: %q", backup.BackupPath, data)
		}
		if mode := testFileMode(t, backup.BackupPath); mode != 0o600 {
			t.Fatalf("backup mode = %o, want 600", mode)
		}
	}
}

func TestRestoreToolFileRejectsUnrelatedBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	backup := filepath.Join(dir, "other.json.sub2api-20260101T000000.000000000Z.bak")
	if err := os.WriteFile(target, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := RestoreToolFile(context.Background(), backup, target)
	if err == nil || !errors.Is(err, ErrInvalidToolPath) {
		t.Fatalf("restore error = %v, want ErrInvalidToolPath", err)
	}
	assertFileContents(t, target, "current")
}
