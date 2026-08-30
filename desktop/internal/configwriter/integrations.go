package configwriter

// This file contains the optional local-tool integrations exposed by the
// desktop shell.  They intentionally live next to the connection metadata
// writer, but never put the API key in connection.json.  Claude Code still
// consumes its native environment settings, while Codex uses an env_key and
// the desktop launcher injects the value at process start; Codex auth.json is
// deliberately left untouched so a secret is not copied into plaintext files
// or backups.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

var (
	ErrUnsupportedTool  = errors.New("unsupported local tool")
	ErrInvalidToolPath  = errors.New("local tool path is invalid")
	ErrInvalidToolInput = errors.New("local tool configuration is invalid")
	ErrConcurrentChange = errors.New("local tool configuration changed during integration")
)

// A single process-wide critical section protects the read/merge/backup/write
// transaction across separate App bindings and separate JSONWriter instances.
// Wails is configured as a single instance as well, but keeping this guard at
// the package boundary protects tests and embedders that construct multiple
// writers themselves.
var toolIntegrationMu sync.Mutex

// Tool identifies a supported coding client.
type Tool string

const (
	ToolCodex  Tool = "codex"
	ToolClaude Tool = "claude"
)

// ToolIntegrationInput is deliberately small and serializable. HomeDir is
// optional and exists for tests/portable installations; the App binding leaves
// it empty so the native platform home directory is used.
type ToolIntegrationInput struct {
	Tool    Tool   `json:"tool"`
	HomeDir string `json:"home_dir,omitempty"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model,omitempty"`
}

// ToolFileChange describes one file touched by an integration. It never
// contains file contents or the secret itself.
type ToolFileChange struct {
	Path           string `json:"path"`
	BackupPath     string `json:"backup_path,omitempty"`
	Changed        bool   `json:"changed"`
	ContainsSecret bool   `json:"contains_secret"`
}

type ToolIntegrationResult struct {
	Tool        Tool             `json:"tool"`
	Files       []ToolFileChange `json:"files"`
	Warnings    []string         `json:"warnings,omitempty"`
	CompletedAt time.Time        `json:"completed_at"`
}

// ToolBackup is a recoverable copy made immediately before a file change.
// Backups are sibling files with mode 0600, so a failed integration can be
// rolled back without exposing the API key to another user on the machine.
type ToolBackup struct {
	OriginalPath string    `json:"original_path"`
	BackupPath   string    `json:"backup_path"`
	CreatedAt    time.Time `json:"created_at"`
}

// ToolPaths resolves the native config locations for a home directory.
func ToolPaths(tool Tool, homeDir string) (map[string]string, error) {
	home, err := resolveHomeDir(homeDir)
	if err != nil {
		return nil, err
	}
	switch Tool(strings.ToLower(strings.TrimSpace(string(tool)))) {
	case ToolCodex:
		return map[string]string{
			"config": filepath.Join(home, ".codex", "config.toml"),
			"auth":   filepath.Join(home, ".codex", "auth.json"),
		}, nil
	case ToolClaude:
		return map[string]string{
			"settings": filepath.Join(home, ".claude", "settings.json"),
		}, nil
	default:
		return nil, ErrUnsupportedTool
	}
}

// IntegrateTool merges the requested endpoint and key into the existing
// Codex/Claude configuration while preserving unrelated user settings.
func IntegrateTool(ctx context.Context, input ToolIntegrationInput) (ToolIntegrationResult, error) {
	if err := contextErr(ctx); err != nil {
		return ToolIntegrationResult{}, err
	}
	toolIntegrationMu.Lock()
	defer toolIntegrationMu.Unlock()
	tool := Tool(strings.ToLower(strings.TrimSpace(string(input.Tool))))
	if tool != ToolCodex && tool != ToolClaude {
		return ToolIntegrationResult{}, ErrUnsupportedTool
	}
	baseURL, err := normalizeToolURL(input.BaseURL)
	if err != nil {
		return ToolIntegrationResult{}, err
	}
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" || strings.IndexFunc(apiKey, func(r rune) bool { return r == '\r' || r == '\n' || r == 0 }) >= 0 {
		return ToolIntegrationResult{}, fmt.Errorf("%w: API key is empty or contains control characters", ErrInvalidToolInput)
	}
	paths, err := ToolPaths(tool, input.HomeDir)
	if err != nil {
		return ToolIntegrationResult{}, err
	}
	// Codex authentication is supplied by the native launcher through the
	// process environment.  Do not touch auth.json: writing the full key there
	// would defeat the OS-keyring boundary and copy it into backups.
	targetKeys := []string{"settings"}
	if tool == ToolCodex {
		targetKeys = []string{"config"}
	}
	lockTargets := make([]string, 0, len(targetKeys))
	for _, key := range targetKeys {
		lockTargets = append(lockTargets, paths[key])
	}
	locks, err := acquireFileLocks(ctx, lockTargets)
	if err != nil {
		return ToolIntegrationResult{}, err
	}
	defer func() { _ = releaseFileLocks(locks) }()
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = "gpt-5.5"
	}
	if tool == ToolCodex {
		baseURL = ensureV1Path(baseURL)
	}

	// Capture every target before parsing/merging. A later hash comparison
	// prevents a CLI or another process from being silently overwritten while
	// this transaction is in flight.
	snapshots := make(map[string]toolFileSnapshot, len(lockTargets))
	for _, path := range lockTargets {
		snapshot, snapshotErr := snapshotToolFile(path)
		if snapshotErr != nil {
			return ToolIntegrationResult{}, snapshotErr
		}
		snapshots[path] = snapshot
	}

	// Build every output before touching disk. This prevents a malformed
	// existing second file from leaving the first file half-updated.
	outputs := make([]preparedToolFile, 0, len(paths))
	if tool == ToolCodex {
		configData, configChanged, err := mergeCodexConfig(paths["config"], baseURL, model)
		if err != nil {
			return ToolIntegrationResult{}, err
		}
		outputs = append(outputs, preparedToolFile{path: paths["config"], data: configData, changed: configChanged, containsSecret: false, validate: validateCodexConfig})
	} else {
		settingsData, changed, err := mergeClaudeSettings(paths["settings"], baseURL, apiKey)
		if err != nil {
			return ToolIntegrationResult{}, err
		}
		outputs = append(outputs, preparedToolFile{path: paths["settings"], data: settingsData, changed: changed, containsSecret: true, validate: validateJSON})
	}

	result := ToolIntegrationResult{Tool: tool, Files: make([]ToolFileChange, 0, len(outputs)), CompletedAt: time.Now().UTC()}
	if tool == ToolCodex {
		result.Warnings = append(result.Warnings, "Codex provider 使用 env_key=SUB2API_API_KEY；桌面端不会修改 auth.json，启动时请使用安全启动指令按需注入密钥。")
	}
	backups := make([]ToolBackup, 0, len(outputs))
	applied := make([]appliedToolFile, 0, len(outputs))
	for _, output := range outputs {
		if err := contextErr(ctx); err != nil {
			rollbackToolFiles(applied)
			return ToolIntegrationResult{}, err
		}
		if !output.changed {
			result.Files = append(result.Files, ToolFileChange{Path: output.path, Changed: false, ContainsSecret: output.containsSecret})
			continue
		}
		current, snapshotErr := snapshotToolFile(output.path)
		if snapshotErr != nil {
			rollbackToolFiles(applied)
			return ToolIntegrationResult{}, snapshotErr
		}
		if !current.equal(snapshots[output.path]) {
			rollbackToolFiles(applied)
			return ToolIntegrationResult{}, fmt.Errorf("%w: %s", ErrConcurrentChange, output.path)
		}
		backup, err := backupToolFileLocked(ctx, output.path)
		hadOriginal := err == nil
		if err != nil {
			// A missing file is a valid first-install case; BackupToolFile
			// returns a typed not-exist signal so no empty backup is reported.
			if !errors.Is(err, fs.ErrNotExist) {
				rollbackToolFiles(applied)
				return ToolIntegrationResult{}, err
			}
		} else {
			backups = append(backups, backup)
		}
		if err := writeToolFile(ctx, output.path, output.data); err != nil {
			// Best-effort rollback of files already changed in this operation.
			rollbackToolFiles(applied)
			if !hadOriginal {
				_ = removeToolFileIfMatches(output.path, output.data)
			}
			return ToolIntegrationResult{}, err
		}
		applied = append(applied, appliedToolFile{output: output, backup: backup, hadOriginal: hadOriginal})
		if output.validate != nil {
			written, readErr := os.ReadFile(output.path)
			if readErr != nil {
				rollbackToolFiles(applied)
				return ToolIntegrationResult{}, fmt.Errorf("verify %s after write: %w", output.path, readErr)
			}
			if !bytes.Equal(written, output.data) {
				rollbackToolFiles(applied)
				return ToolIntegrationResult{}, fmt.Errorf("verify %s after write: %w", output.path, ErrConcurrentChange)
			}
			if validateErr := output.validate(written); validateErr != nil {
				rollbackToolFiles(applied)
				return ToolIntegrationResult{}, fmt.Errorf("verify %s after write: %w", output.path, validateErr)
			}
		}
		change := ToolFileChange{Path: output.path, Changed: true, ContainsSecret: output.containsSecret}
		if hadOriginal {
			change.BackupPath = backup.BackupPath
		}
		result.Files = append(result.Files, change)
	}
	return result, nil
}

// BackupToolFile makes a timestamped sibling backup. It rejects symlinks so a
// malicious config path cannot make the desktop app copy an unrelated file.
func BackupToolFile(ctx context.Context, path string) (ToolBackup, error) {
	if err := contextErr(ctx); err != nil {
		return ToolBackup{}, err
	}
	path, err := cleanToolPath(path)
	if err != nil {
		return ToolBackup{}, err
	}
	lock, err := acquireFileLock(ctx, path)
	if err != nil {
		return ToolBackup{}, err
	}
	defer func() { _ = lock.Close() }()
	return backupToolFileLocked(ctx, path)
}

func backupToolFileLocked(ctx context.Context, path string) (ToolBackup, error) {
	if err := contextErr(ctx); err != nil {
		return ToolBackup{}, err
	}
	if err := inspectSafeParent(path); err != nil {
		return ToolBackup{}, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	exists, err := inspectSafeTarget(path, false)
	if err != nil || !exists {
		if err != nil {
			// Preserve fs.ErrNotExist so first-install integrations can proceed
			// without manufacturing an empty backup, while still exposing the
			// typed tool-path error to callers that inspect safety failures.
			return ToolBackup{}, fmt.Errorf("%w: %w", ErrInvalidToolPath, err)
		}
		return ToolBackup{}, fmt.Errorf("%w: %s is not a regular file", ErrInvalidToolPath, path)
	}
	backupPath, err := reserveBackupPath(path)
	if err != nil {
		return ToolBackup{}, err
	}
	if err := copyToolFile(backupPath, path, 0o600); err != nil {
		// The reservation is an empty regular file. Remove it only if it is
		// still that file; never follow or delete a path that changed type.
		_ = removeReservedBackup(backupPath)
		return ToolBackup{}, fmt.Errorf("backup %s: %w", path, err)
	}
	return ToolBackup{OriginalPath: path, BackupPath: backupPath, CreatedAt: time.Now().UTC()}, nil
}

func reserveBackupPath(path string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	base := path + ".sub2api-" + stamp
	for index := 0; index < 1000; index++ {
		candidate := base + ".bak"
		if index > 0 {
			candidate = fmt.Sprintf("%s-%d.bak", base, index)
		}
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(candidate)
				return "", closeErr
			}
			return candidate, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return "", fmt.Errorf("reserve backup %s: %w", path, err)
	}
	return "", fmt.Errorf("reserve backup %s: too many timestamp collisions", path)
}

func removeReservedBackup(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return nil
	}
	return os.Remove(path)
}

// RestoreToolFile restores a backup and first snapshots the current target.
// The returned backup is useful for an undo action if the user changes their
// mind; no destructive deletion is performed.
func RestoreToolFile(ctx context.Context, backupPath, targetPath string) (ToolBackup, error) {
	if err := contextErr(ctx); err != nil {
		return ToolBackup{}, err
	}
	toolIntegrationMu.Lock()
	defer toolIntegrationMu.Unlock()
	backupPath, err := cleanToolPath(backupPath)
	if err != nil {
		return ToolBackup{}, err
	}
	targetPath, err = cleanToolPath(targetPath)
	if err != nil {
		return ToolBackup{}, err
	}
	if filepath.Dir(backupPath) != filepath.Dir(targetPath) ||
		!strings.HasPrefix(filepath.Base(backupPath), filepath.Base(targetPath)+".sub2api-") ||
		!strings.HasSuffix(backupPath, ".bak") {
		return ToolBackup{}, fmt.Errorf("%w: backup is not generated for target", ErrInvalidToolPath)
	}
	locks, err := acquireFileLocks(ctx, []string{backupPath, targetPath})
	if err != nil {
		return ToolBackup{}, err
	}
	defer func() { _ = releaseFileLocks(locks) }()
	backupSnapshot, err := snapshotToolFile(backupPath)
	if err != nil {
		return ToolBackup{}, err
	}
	targetSnapshot, err := snapshotToolFile(targetPath)
	if err != nil {
		return ToolBackup{}, err
	}
	if err := inspectSafeParent(backupPath); err != nil {
		return ToolBackup{}, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	if err := inspectSafeParent(targetPath); err != nil {
		return ToolBackup{}, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	backupInfo, err := os.Lstat(backupPath)
	if err != nil {
		return ToolBackup{}, err
	}
	if !backupInfo.Mode().IsRegular() {
		return ToolBackup{}, fmt.Errorf("%w: backup is not a regular file", ErrInvalidToolPath)
	}
	var current ToolBackup
	if _, statErr := os.Lstat(targetPath); statErr == nil {
		current, err = backupToolFileLocked(ctx, targetPath)
		if err != nil {
			return ToolBackup{}, err
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return ToolBackup{}, statErr
	}
	// Recheck both inputs after the backup operation. This catches tools that
	// ignore the advisory lock and mutate a file while we are preparing the
	// replacement. Never restore a stale or attacker-modified snapshot.
	if latest, latestErr := snapshotToolFile(backupPath); latestErr != nil {
		return ToolBackup{}, latestErr
	} else if !latest.equal(backupSnapshot) {
		return ToolBackup{}, fmt.Errorf("%w: %s", ErrConcurrentChange, backupPath)
	}
	if latest, latestErr := snapshotToolFile(targetPath); latestErr != nil {
		return ToolBackup{}, latestErr
	} else if !latest.equal(targetSnapshot) {
		return ToolBackup{}, fmt.Errorf("%w: %s", ErrConcurrentChange, targetPath)
	}
	if err := restoreToolFile(backupPath, targetPath); err != nil {
		return ToolBackup{}, err
	}
	if restored, verifyErr := snapshotToolFile(targetPath); verifyErr != nil {
		return ToolBackup{}, fmt.Errorf("verify restored %s: %w", targetPath, verifyErr)
	} else if !restored.equal(backupSnapshot) {
		return ToolBackup{}, fmt.Errorf("verify restored %s: %w", targetPath, ErrConcurrentChange)
	}
	return current, nil
}

type preparedToolFile struct {
	path           string
	data           []byte
	changed        bool
	containsSecret bool
	validate       func([]byte) error
}

type toolFileSnapshot struct {
	exists bool
	hash   [sha256.Size]byte
}

func snapshotToolFile(path string) (toolFileSnapshot, error) {
	path, err := cleanToolPath(path)
	if err != nil {
		return toolFileSnapshot{}, err
	}
	if err := inspectSafeParent(path); err != nil {
		return toolFileSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	exists, err := inspectSafeTarget(path, true)
	if err != nil {
		return toolFileSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	if !exists {
		return toolFileSnapshot{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return toolFileSnapshot{}, fmt.Errorf("read %s: %w", path, err)
	}
	return toolFileSnapshot{exists: true, hash: sha256.Sum256(data)}, nil
}

func (s toolFileSnapshot) equal(other toolFileSnapshot) bool {
	return s.exists == other.exists && (!s.exists || s.hash == other.hash)
}

func validateJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func validateCodexConfig(data []byte) error {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("invalid TOML: %w", err)
	}
	if doc["model_provider"] != "sub2api" {
		return errors.New("managed model_provider was not persisted")
	}
	providers, ok := doc["model_providers"].(map[string]any)
	if !ok {
		return errors.New("managed model_providers was not persisted")
	}
	provider, ok := providers["sub2api"].(map[string]any)
	if !ok || provider["base_url"] == "" || provider["wire_api"] != "responses" || provider["env_key"] != "SUB2API_API_KEY" || provider["requires_openai_auth"] != false {
		return errors.New("managed Codex provider fields were not persisted")
	}
	if _, exists := provider["experimental_bearer_token"]; exists {
		return errors.New("reserved experimental bearer token must not be persisted")
	}
	return nil
}

type appliedToolFile struct {
	output      preparedToolFile
	backup      ToolBackup
	hadOriginal bool
}

func rollbackToolFiles(files []appliedToolFile) {
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		// Roll back only when the file still contains the bytes written by this
		// transaction. A non-cooperating process may have edited it after our
		// post-write validation; clobbering that newer content would be worse
		// than leaving the failed integration visible for manual recovery.
		if current, err := snapshotToolFile(file.output.path); err != nil || !current.equal(snapshotForData(file.output.data)) {
			continue
		}
		if file.hadOriginal {
			_ = restoreToolFile(file.backup.BackupPath, file.output.path)
		} else {
			_ = removeToolFileIfMatches(file.output.path, file.output.data)
		}
	}
}

func snapshotForData(data []byte) toolFileSnapshot {
	return toolFileSnapshot{exists: true, hash: sha256.Sum256(data)}
}

func removeToolFileIfMatches(path string, expectedData []byte) error {
	current, err := snapshotToolFile(path)
	if err != nil {
		return err
	}
	if !current.equal(snapshotForData(expectedData)) {
		return fmt.Errorf("%w: %s", ErrConcurrentChange, path)
	}
	return os.Remove(path)
}

func mergeCodexConfig(path, baseURL, model string) ([]byte, bool, error) {
	doc, existed, err := readTOMLDocument(path)
	if err != nil {
		return nil, false, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	changed := !existed
	if value, ok := doc["model_provider"].(string); !ok || value != "sub2api" {
		doc["model_provider"] = "sub2api"
		changed = true
	}
	if value, ok := doc["model"].(string); !ok || strings.TrimSpace(value) == "" {
		doc["model"] = model
		changed = true
	}
	providers, ok := doc["model_providers"].(map[string]any)
	if !ok || providers == nil {
		providers = map[string]any{}
		doc["model_providers"] = providers
		changed = true
	}
	provider, ok := providers["sub2api"].(map[string]any)
	if !ok || provider == nil {
		provider = map[string]any{}
		providers["sub2api"] = provider
		changed = true
	}
	for key, value := range map[string]any{
		"name":                 "Sub2API",
		"base_url":             baseURL,
		"wire_api":             "responses",
		"env_key":              "SUB2API_API_KEY",
		"requires_openai_auth": false,
	} {
		if !sameScalar(provider[key], value) {
			provider[key] = value
			changed = true
		}
	}
	if _, exists := provider["experimental_bearer_token"]; exists {
		delete(provider, "experimental_bearer_token")
		changed = true
	}
	data, err := toml.Marshal(doc)
	if err != nil {
		return nil, false, fmt.Errorf("encode Codex config: %w", err)
	}
	data = append(data, '\n')
	if existed {
		if old, readErr := os.ReadFile(path); readErr == nil {
			changed = changed || !bytes.Equal(old, data)
		}
	}
	return data, changed, nil
}

func mergeClaudeSettings(path, baseURL, apiKey string) ([]byte, bool, error) {
	doc, existed, err := readJSONDocument(path)
	if err != nil {
		return nil, false, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	env, ok := doc["env"].(map[string]any)
	if !ok || env == nil {
		env = map[string]any{}
		doc["env"] = env
	}
	values := map[string]string{
		"ANTHROPIC_BASE_URL":                       baseURL,
		"ANTHROPIC_AUTH_TOKEN":                     apiKey,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"CLAUDE_CODE_ATTRIBUTION_HEADER":           "0",
	}
	changed := !existed
	for key, value := range values {
		if !sameScalar(env[key], value) {
			env[key] = value
			changed = true
		}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("encode Claude settings: %w", err)
	}
	data = append(data, '\n')
	if existed {
		if old, readErr := os.ReadFile(path); readErr == nil {
			changed = changed || !bytes.Equal(old, data)
		}
	}
	return data, changed, nil
}

func readJSONDocument(path string) (map[string]any, bool, error) {
	if err := inspectSafeParent(path); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	exists, err := inspectSafeTarget(path, true)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	if !exists {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, true, fmt.Errorf("decode %s: %w", path, err)
	}
	return doc, true, nil
}

func readTOMLDocument(path string) (map[string]any, bool, error) {
	if err := inspectSafeParent(path); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	exists, err := inspectSafeTarget(path, true)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidToolPath, err)
	}
	if !exists {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, true, fmt.Errorf("decode %s: %w", path, err)
	}
	return doc, true, nil
}

func writeToolFile(ctx context.Context, path string, data []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	path, err := cleanToolPath(path)
	if err != nil {
		return err
	}
	if err := ensureSafeParent(path, 0o700); err != nil {
		return fmt.Errorf("create tool config directory: %w", err)
	}
	if _, err := inspectSafeTarget(path, true); err != nil {
		return fmt.Errorf("%w: refusing to replace non-regular file %s", ErrInvalidToolPath, path)
	}
	return writeSecureAtomic(path, data, 0o600, ".sub2api-tool-*.tmp")
}

func copyToolFile(destination, source string, mode fs.FileMode) error {
	if err := inspectSafeParent(source); err != nil {
		return err
	}
	if exists, err := inspectSafeTarget(source, false); err != nil || !exists {
		if err != nil {
			return err
		}
		return fs.ErrNotExist
	}
	before, err := snapshotToolFile(source)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	after, err := snapshotToolFile(source)
	if err != nil {
		return err
	}
	if !before.equal(after) {
		return fmt.Errorf("%w: %s", ErrConcurrentChange, source)
	}
	return writeSecureAtomic(destination, data, mode, ".sub2api-backup-*.tmp")
}

func restoreToolFile(backupPath, targetPath string) error {
	if err := inspectSafeParent(backupPath); err != nil {
		return err
	}
	if exists, err := inspectSafeTarget(backupPath, false); err != nil || !exists {
		if err != nil {
			return err
		}
		return fs.ErrNotExist
	}
	before, err := snapshotToolFile(backupPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	after, err := snapshotToolFile(backupPath)
	if err != nil {
		return err
	}
	if !before.equal(after) {
		return fmt.Errorf("%w: %s", ErrConcurrentChange, backupPath)
	}
	return writeSecureAtomic(targetPath, data, 0o600, ".sub2api-restore-*.tmp")
}

func resolveHomeDir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("%w: home directory unavailable", ErrInvalidToolPath)
		}
		value = home
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil || abs == "." || abs == string(filepath.Separator) {
		return "", ErrInvalidToolPath
	}
	return abs, nil
}

func cleanToolPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrInvalidToolPath
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil || abs == "." || abs == string(filepath.Separator) {
		return "", ErrInvalidToolPath
	}
	return abs, nil
}

func normalizeToolURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base URL must be an http(s) origin without credentials/query", ErrInvalidToolInput)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: base URL must use http or https", ErrInvalidToolInput)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func sameScalar(left, right any) bool {
	return reflect.DeepEqual(left, right)
}

func ensureV1Path(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return strings.TrimRight(value, "/") + "/v1"
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/v1") && path != "/v1" {
		path += "/v1"
	}
	if path == "" {
		path = "/v1"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String()
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
