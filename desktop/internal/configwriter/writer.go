// Package configwriter persists non-sensitive desktop preferences atomically.
package configwriter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidConfigPath = errors.New("config path is required")
	ErrUnsafeConfigPath  = errors.New("config path is unsafe")
)

// ConnectionConfig intentionally stores only a reference to the API key. The
// key itself belongs in securestore and must never be serialized here.
type ConnectionConfig struct {
	SchemaVersion int    `json:"schema_version"`
	SiteURL       string `json:"site_url"`
	GatewayURL    string `json:"gateway_url"`
	AuthMode      string `json:"auth_mode,omitempty"`
	APIKeyRef     string `json:"api_key_ref,omitempty"`
	// APIKeyID is the selected image/gateway key id. Only ids and masked hints
	// are persisted here; the corresponding secret stays in securestore.
	APIKeyID        int64  `json:"api_key_id,omitempty"`
	CodexAPIKeyID   int64  `json:"codex_api_key_id,omitempty"`
	ClaudeAPIKeyID  int64  `json:"claude_api_key_id,omitempty"`
	CodexAPIKeyRef  string `json:"codex_api_key_ref,omitempty"`
	ClaudeAPIKeyRef string `json:"claude_api_key_ref,omitempty"`
	APIKeyHint      string `json:"api_key_hint,omitempty"`
	AccessTokenRef  string `json:"access_token_ref,omitempty"`
	RefreshTokenRef string `json:"refresh_token_ref,omitempty"`
	DPoPKeyRef      string `json:"dpop_key_ref,omitempty"`
	DPoPNonce       string `json:"dpop_nonce,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	// AccountOwnerHash is a domain-separated digest of the authenticated
	// account subject. It partitions local task checkpoints without persisting
	// a raw user id/email and is refreshed only from a successful profile call.
	AccountOwnerHash string    `json:"account_owner_hash,omitempty"`
	ProtectionLevel  string    `json:"protection_level,omitempty"`
	Scope            string    `json:"scope,omitempty"`
	Label            string    `json:"label,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Writer interface {
	Load(ctx context.Context) (ConnectionConfig, error)
	Save(ctx context.Context, config ConnectionConfig) error
	Clear(ctx context.Context) error
	Path() string
}

type JSONWriter struct {
	path string
	mu   sync.Mutex
}

func NewJSONWriter(path string) (*JSONWriter, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, ErrInvalidConfigPath
	}
	path = filepath.Clean(path)
	if err := inspectSafeParent(path); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsafeConfigPath, err)
	}
	if _, err := inspectSafeTarget(path, true); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsafeConfigPath, err)
	}
	return &JSONWriter{path: path}, nil
}

func (w *JSONWriter) Path() string { return w.path }

func (w *JSONWriter) Load(ctx context.Context) (ConnectionConfig, error) {
	if err := contextErr(ctx); err != nil {
		return ConnectionConfig{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	lock, err := acquireFileLock(ctx, w.path)
	if err != nil {
		return ConnectionConfig{}, err
	}
	defer func() { _ = lock.Close() }()
	if err := inspectSafeParent(w.path); err != nil {
		return ConnectionConfig{}, fmt.Errorf("%w: %v", ErrUnsafeConfigPath, err)
	}
	exists, err := inspectSafeTarget(w.path, true)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("%w: %v", ErrUnsafeConfigPath, err)
	}
	if !exists {
		return ConnectionConfig{}, nil
	}
	data, err := os.ReadFile(w.path)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("read desktop config: %w", err)
	}
	var config ConnectionConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return ConnectionConfig{}, fmt.Errorf("decode desktop config: %w", err)
	}
	return config, nil
}

func (w *JSONWriter) Save(ctx context.Context, config ConnectionConfig) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(config.SiteURL) == "" {
		return errors.New("site URL is required")
	}
	if strings.TrimSpace(config.APIKeyRef) == "" && strings.TrimSpace(config.RefreshTokenRef) == "" {
		return errors.New("API key or refresh token reference is required")
	}
	if config.SchemaVersion == 0 {
		config.SchemaVersion = 1
	}
	if config.UpdatedAt.IsZero() {
		config.UpdatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode desktop config: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ensureSafeParent(w.path, 0o700); err != nil {
		return fmt.Errorf("create desktop config directory: %w", err)
	}
	lock, err := acquireFileLock(ctx, w.path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	initial, err := snapshotToolFile(w.path)
	if err != nil {
		return fmt.Errorf("snapshot desktop config: %w", err)
	}
	exists, err := inspectSafeTarget(w.path, true)
	if err != nil {
		return fmt.Errorf("inspect desktop config: %w", err)
	}
	hadOriginal := exists
	backupPath := w.path + ".bak"
	if exists {
		// Keep a recoverable metadata-only backup before replacing the file.
		if err := copyFile(backupPath, w.path); err != nil {
			return fmt.Errorf("backup desktop config: %w", err)
		}
	}
	current, err := snapshotToolFile(w.path)
	if err != nil {
		return fmt.Errorf("recheck desktop config: %w", err)
	}
	if !current.equal(initial) {
		return fmt.Errorf("%w: %s", ErrConcurrentChange, w.path)
	}
	if err := writeSecureAtomic(w.path, data, 0o600, ".connection-*.tmp"); err != nil {
		return fmt.Errorf("write desktop config: %w", err)
	}
	written, err := os.ReadFile(w.path)
	if err != nil {
		return rollbackJSONWriterSave(w.path, backupPath, hadOriginal, data, fmt.Errorf("verify desktop config after write: %w", err))
	}
	var verified ConnectionConfig
	if err := json.Unmarshal(written, &verified); err != nil {
		return rollbackJSONWriterSave(w.path, backupPath, hadOriginal, data, fmt.Errorf("verify desktop config after write: %w", err))
	}
	if strings.TrimSpace(verified.SiteURL) == "" || (strings.TrimSpace(verified.APIKeyRef) == "" && strings.TrimSpace(verified.RefreshTokenRef) == "") {
		return rollbackJSONWriterSave(w.path, backupPath, hadOriginal, data, errors.New("verify desktop config after write: required fields are missing"))
	}
	return nil
}

func rollbackJSONWriterSave(targetPath, backupPath string, hadOriginal bool, expectedData []byte, cause error) error {
	var rollbackErr error
	current, snapshotErr := snapshotToolFile(targetPath)
	expected := toolFileSnapshot{exists: true, hash: sha256.Sum256(expectedData)}
	if snapshotErr != nil {
		rollbackErr = snapshotErr
	} else if !current.equal(expected) {
		// Another process changed the file after our write. Do not overwrite
		// that newer content while attempting a rollback.
		return errors.Join(cause, fmt.Errorf("%w: %s", ErrConcurrentChange, targetPath))
	} else if hadOriginal {
		rollbackErr = restoreToolFile(backupPath, targetPath)
	} else {
		rollbackErr = os.Remove(targetPath)
	}
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("rollback desktop config: %w", rollbackErr))
	}
	return cause
}

func (w *JSONWriter) Clear(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	lock, err := acquireFileLock(ctx, w.path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := inspectSafeParent(w.path); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeConfigPath, err)
	}
	initial, err := snapshotToolFile(w.path)
	if err != nil {
		return fmt.Errorf("snapshot desktop config: %w", err)
	}
	exists, err := inspectSafeTarget(w.path, true)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeConfigPath, err)
	}
	if !exists {
		return nil
	}
	current, err := snapshotToolFile(w.path)
	if err != nil {
		return fmt.Errorf("recheck desktop config: %w", err)
	}
	if !current.equal(initial) {
		return fmt.Errorf("%w: %s", ErrConcurrentChange, w.path)
	}
	if err := os.Remove(w.path); err != nil {
		return fmt.Errorf("remove desktop config: %w", err)
	}
	return nil
}

func copyFile(destination, source string) error {
	if err := inspectSafeParent(source); err != nil {
		return err
	}
	if exists, err := inspectSafeTarget(source, false); err != nil || !exists {
		if err != nil {
			return err
		}
		return fs.ErrNotExist
	}
	if err := ensureSafeParent(destination, 0o700); err != nil {
		return err
	}
	if _, err := inspectSafeTarget(destination, true); err != nil {
		return err
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
	return writeSecureAtomic(destination, data, 0o600, ".connection-backup-*.tmp")
}

// inspectSafeParent verifies every existing component of a path's parent.
// Missing components are allowed so callers can create a new private config
// tree; any symlink or non-directory component is rejected. Walking each
// component explicitly is important: os.Lstat on a deep path otherwise
// follows symlinks in intermediate directories.
func inspectSafeParent(path string) error {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	return inspectSafeDirectory(filepath.Dir(absPath), false, 0)
}

// ensureSafeParent creates missing parent components one at a time after
// checking every existing ancestor, avoiding MkdirAll symlink follows.
func ensureSafeParent(path string, mode fs.FileMode) error {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	return inspectSafeDirectory(filepath.Dir(absPath), true, mode)
}

// inspectSafeDirectory walks an absolute directory path component by
// component. If createMissing is true, absent components are created with the
// requested mode and immediately checked with Lstat before continuing.
func inspectSafeDirectory(path string, createMissing bool, mode fs.FileMode) error {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	absPath = normalizeSystemPath(absPath)
	root := filepath.VolumeName(absPath) + string(os.PathSeparator)
	if filepath.VolumeName(absPath) == "" {
		root = string(os.PathSeparator)
	}
	relative, err := filepath.Rel(root, absPath)
	if err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) && createMissing {
			if mkdirErr := os.Mkdir(current, mode); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			// Once one component is missing, all following components are
			// necessarily missing for this path. The caller that creates files
			// will revisit them one at a time.
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("parent %s is not a real directory", current)
		}
	}
	return nil
}

// macOS exposes /var (and sometimes /tmp) as compatibility symlinks into
// /private. They are OS-owned aliases, not user-controlled config parents;
// normalize them before the component walk so normal temporary/config paths
// remain usable while all other symlinks are still rejected.
func normalizeSystemPath(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	for _, alias := range []string{"/var", "/tmp"} {
		if path == alias || strings.HasPrefix(path, alias+string(os.PathSeparator)) {
			return "/private" + path
		}
	}
	return path
}

// inspectSafeTarget checks a file without following symlinks. The boolean
// reports whether a regular target exists; allowMissing controls whether a
// missing path is accepted.
func inspectSafeTarget(path string, allowMissing bool) (bool, error) {
	if err := inspectSafeParent(path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if allowMissing {
			return false, nil
		}
		return false, fs.ErrNotExist
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("target %s is not a regular file", path)
	}
	return true, nil
}

// writeSecureAtomic writes through a private temporary sibling and replaces
// the target atomically. It rechecks the parent/target immediately before the
// rename so a newly introduced symlink is never followed.
func writeSecureAtomic(path string, data []byte, mode fs.FileMode, tempPattern string) error {
	if err := ensureSafeParent(path, 0o700); err != nil {
		return err
	}
	if _, err := inspectSafeTarget(path, true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := inspectSafeParent(path); err != nil {
		return err
	}
	if _, err := inspectSafeTarget(path, true); err != nil {
		return err
	}
	// The platform helper uses rename(2) on Unix and ReplaceFile/MoveFileEx
	// with replacement semantics on Windows. Never delete the live target as
	// a fallback: a crash between delete and rename would lose the user's
	// configuration.
	if err := replaceFileAtomically(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
