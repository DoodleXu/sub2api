package imagestore

// SQLiteTaskStore is the durable local checkpoint store used by the desktop
// application.  The database contains only task metadata and secure-store
// references; API keys, access tokens and image bytes never enter SQLite.
// modernc.org/sqlite is a pure-Go driver, which keeps the same implementation
// usable by the Windows amd64 and macOS arm64 build targets.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrUnsafeSQLitePath indicates that the database or one of SQLite's sidecar
// files is a symlink/non-regular file. Following such a path could redirect
// private task metadata into an attacker-controlled location.
var ErrUnsafeSQLitePath = errors.New("sqlite task store path is unsafe")

// SQLiteTaskStore persists resumable image-task checkpoints in a private
// application directory.  A single writer connection avoids SQLite lock
// contention while still allowing concurrent read calls from Wails bindings.
type SQLiteTaskStore struct {
	db   *sql.DB
	path string
}

// NewSQLiteTaskStore opens (and, if needed, initializes) a task database.
func NewSQLiteTaskStore(path string) (*SQLiteTaskStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("sqlite task store path is required")
	}
	path = filepath.Clean(path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite task store path: %w", err)
	}
	path = absPath
	if err := ensureSQLiteDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create sqlite task directory: %w", err)
	}
	if err := hardenSQLitePath(path); err != nil {
		return nil, err
	}
	// Pre-create the database with restrictive permissions. SQLite itself may
	// honor a permissive process umask when creating a new file; O_EXCL also
	// makes a last-second symlink at the database path fail closed.
	if exists, statErr := os.Lstat(path); statErr == nil {
		if !exists.Mode().IsRegular() || exists.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: database path is not a regular file", ErrUnsafeSQLitePath)
		}
	} else if errors.Is(statErr, fs.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("%w: create database file: %v", ErrUnsafeSQLitePath, createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("close database file: %w", closeErr)
		}
	} else {
		return nil, fmt.Errorf("%w: inspect database path: %v", ErrUnsafeSQLitePath, statErr)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite task store: %w", err)
	}
	// SQLite serializes writers.  A bounded pool plus a busy timeout gives the
	// UI deterministic behavior when a refresh and a poll finish together.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteTaskStore{db: db, path: path}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.hardenSQLiteFiles(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteTaskStore) initialize(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite task store is unavailable")
	}
	statements := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`CREATE TABLE IF NOT EXISTS image_tasks (
			task_id TEXT PRIMARY KEY,
			id TEXT NOT NULL,
			owner_hash TEXT NOT NULL DEFAULT '',
			api_key_id INTEGER NOT NULL DEFAULT 0,
			api_key_ref TEXT NOT NULL,
			site_url TEXT NOT NULL,
			gateway_url TEXT NOT NULL,
			status TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			parameters_json TEXT NOT NULL DEFAULT '',
			assets_downloaded INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS image_tasks_updated_at_idx ON image_tasks(updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sqlite task store: %w", err)
		}
	}
	// Older desktop builds created image_tasks without api_key_id. Keep those
	// checkpoints readable and add the non-secret identifier lazily rather
	// than forcing users to delete their local task history.
	if err := s.ensureAPIKeyIDColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureOwnerHashColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureAssetsDownloadedColumn(ctx); err != nil {
		return err
	}
	return s.hardenSQLiteFiles()
}

func (s *SQLiteTaskStore) ensureAPIKeyIDColumn(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite task store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(image_tasks)`)
	if err != nil {
		return fmt.Errorf("inspect image task schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if scanErr := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("scan image task schema: %w", scanErr)
		}
		if name == "api_key_id" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read image task schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close image task schema: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE image_tasks ADD COLUMN api_key_id INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("migrate image task schema: %w", err)
	}
	return nil
}

// ensureOwnerHashColumn upgrades databases created by pre-ownership desktop
// builds. Existing rows intentionally receive the empty default and therefore
// remain unowned; no migration code may infer the currently signed-in user.
func (s *SQLiteTaskStore) ensureOwnerHashColumn(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite task store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(image_tasks)`)
	if err != nil {
		return fmt.Errorf("inspect image task owner schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if scanErr := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("scan image task owner schema: %w", scanErr)
		}
		if name == "owner_hash" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read image task owner schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close image task owner schema: %w", err)
	}
	if found {
		_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS image_tasks_owner_updated_at_idx ON image_tasks(owner_hash, updated_at DESC)`)
		if err != nil {
			return fmt.Errorf("create image task owner index: %w", err)
		}
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE image_tasks ADD COLUMN owner_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate image task owner schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS image_tasks_owner_updated_at_idx ON image_tasks(owner_hash, updated_at DESC)`); err != nil {
		return fmt.Errorf("create image task owner index: %w", err)
	}
	return nil
}

func (s *SQLiteTaskStore) ensureAssetsDownloadedColumn(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite task store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(image_tasks)`)
	if err != nil {
		return fmt.Errorf("inspect image task asset schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if scanErr := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("scan image task asset schema: %w", scanErr)
		}
		if name == "assets_downloaded" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read image task asset schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close image task asset schema: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE image_tasks ADD COLUMN assets_downloaded INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("migrate image task asset schema: %w", err)
	}
	return nil
}

// MigrateJSON imports the old whole-file checkpoint format once.  Existing
// SQLite rows win, so re-running the app cannot overwrite newer checkpoints
// with stale JSON data.  The source file is intentionally retained for a
// recoverable transition; callers may remove it only after user confirmation.
func (s *SQLiteTaskStore) MigrateJSON(ctx context.Context, legacyPath string) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite task store is unavailable")
	}
	legacyPath = strings.TrimSpace(legacyPath)
	if legacyPath == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Clean(legacyPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy image tasks: %w", err)
	}
	var records []TaskRecord
	if err := unmarshalTaskRecords(data, &records); err != nil {
		return fmt.Errorf("decode legacy image tasks: %w", err)
	}
	for _, record := range records {
		if strings.TrimSpace(record.TaskID) == "" {
			continue
		}
		// Preserve a newer SQLite checkpoint if the legacy file is from an
		// earlier process. A missing row is the only case in which migration
		// writes.
		if _, getErr := s.Get(ctx, record.TaskID); getErr == nil {
			continue
		} else if !errors.Is(getErr, ErrTaskNotFound) {
			return getErr
		}
		if err := s.Put(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func unmarshalTaskRecords(data []byte, records *[]TaskRecord) error {
	if records == nil {
		return errors.New("task records destination is nil")
	}
	// Keep JSON decoding local to this file so the migration does not depend on
	// the mutable behavior of JSONTaskStore's filesystem implementation.
	return json.Unmarshal(data, records)
}

func (s *SQLiteTaskStore) Put(ctx context.Context, task TaskRecord) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return errors.New("sqlite task store is unavailable")
	}
	task.TaskID = strings.TrimSpace(task.TaskID)
	if task.TaskID == "" {
		return errors.New("task id is required")
	}
	if task.OwnerHash != "" {
		ownerHash, err := normalizeOwnerHash(task.OwnerHash)
		if err != nil {
			return err
		}
		task.OwnerHash = ownerHash
	}
	if task.ID == "" {
		task.ID = task.TaskID
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = time.Now().UTC()
	if err := s.hardenSQLiteFiles(); err != nil {
		return err
	}
	// Check ownership and write under one SQLite transaction. This prevents a
	// task id from being replaced by another account between separate reads and
	// writes. An empty legacy owner never equals a scoped owner.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin image task checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existingOwner string
	lookupErr := tx.QueryRowContext(ctx, `SELECT owner_hash FROM image_tasks WHERE task_id = ?`, task.TaskID).Scan(&existingOwner)
	switch {
	case errors.Is(lookupErr, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
		INSERT INTO image_tasks (task_id, id, owner_hash, api_key_id, api_key_ref, site_url, gateway_url, status, prompt, model, parameters_json, assets_downloaded, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			task.TaskID, task.ID, task.OwnerHash, task.APIKeyID, task.APIKeyRef, task.SiteURL, task.GatewayURL, task.Status, task.Prompt, task.Model,
			task.ParametersJSON, boolToSQLite(task.AssetsDownloaded), task.CreatedAt.UTC().Format(time.RFC3339Nano), task.UpdatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("save image task checkpoint: %w", err)
		}
	case lookupErr != nil:
		return fmt.Errorf("inspect image task owner: %w", lookupErr)
	case existingOwner != task.OwnerHash:
		return ErrTaskOwnerMismatch
	default:
		_, err = tx.ExecContext(ctx, `
		UPDATE image_tasks SET id = ?, owner_hash = ?, api_key_id = ?, api_key_ref = ?, site_url = ?, gateway_url = ?, status = ?, prompt = ?, model = ?, parameters_json = ?, assets_downloaded = ?, created_at = ?, updated_at = ?
WHERE task_id = ? AND owner_hash = ?`,
			task.ID, task.OwnerHash, task.APIKeyID, task.APIKeyRef, task.SiteURL, task.GatewayURL, task.Status, task.Prompt, task.Model,
			task.ParametersJSON, boolToSQLite(task.AssetsDownloaded), task.CreatedAt.UTC().Format(time.RFC3339Nano), task.UpdatedAt.UTC().Format(time.RFC3339Nano), task.TaskID, task.OwnerHash)
		if err != nil {
			return fmt.Errorf("update image task checkpoint: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit image task checkpoint: %w", err)
	}
	return s.hardenSQLiteFiles()
}

// PutForOwner writes a checkpoint into one canonical owner partition. Legacy
// unowned rows are never adopted, even when task ids happen to match.
func (s *SQLiteTaskStore) PutForOwner(ctx context.Context, ownerHash string, task TaskRecord) error {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return err
	}
	task.OwnerHash = ownerHash
	return s.putForOwner(ctx, task)
}

// putForOwner is separated only to keep the public Put compatibility method
// small; the transaction in Put already enforces owner equality.
func (s *SQLiteTaskStore) putForOwner(ctx context.Context, task TaskRecord) error {
	return s.Put(ctx, task)
}

func (s *SQLiteTaskStore) Get(ctx context.Context, taskID string) (record TaskRecord, resultErr error) {
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	if s == nil || s.db == nil {
		return TaskRecord{}, errors.New("sqlite task store is unavailable")
	}
	if err := s.hardenSQLiteFiles(); err != nil {
		return TaskRecord{}, err
	}
	defer func() {
		if hardenErr := s.hardenSQLiteFiles(); resultErr == nil && hardenErr != nil {
			resultErr = hardenErr
		}
	}()
	var createdRaw, updatedRaw string
	err := s.db.QueryRowContext(ctx, `
	SELECT task_id, id, owner_hash, api_key_id, api_key_ref, site_url, gateway_url, status, prompt, model, parameters_json, assets_downloaded, created_at, updated_at
	FROM image_tasks WHERE task_id = ?`, strings.TrimSpace(taskID)).Scan(
		&record.TaskID, &record.ID, &record.OwnerHash, &record.APIKeyID, &record.APIKeyRef, &record.SiteURL, &record.GatewayURL, &record.Status,
		&record.Prompt, &record.Model, &record.ParametersJSON, &record.AssetsDownloaded, &createdRaw, &updatedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	if err != nil {
		return TaskRecord{}, fmt.Errorf("read image task checkpoint: %w", err)
	}
	record.CreatedAt = parseTaskTime(createdRaw)
	record.UpdatedAt = parseTaskTime(updatedRaw)
	return record, nil
}

// GetForOwner returns only a row in the requested owner partition. A row with
// another owner (or an empty owner from a legacy build) is indistinguishable
// from a missing row to avoid leaking task existence across accounts.
func (s *SQLiteTaskStore) GetForOwner(ctx context.Context, ownerHash, taskID string) (record TaskRecord, resultErr error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return TaskRecord{}, err
	}
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	if s == nil || s.db == nil {
		return TaskRecord{}, errors.New("sqlite task store is unavailable")
	}
	if err := s.hardenSQLiteFiles(); err != nil {
		return TaskRecord{}, err
	}
	defer func() {
		if hardenErr := s.hardenSQLiteFiles(); resultErr == nil && hardenErr != nil {
			resultErr = hardenErr
		}
	}()
	var createdRaw, updatedRaw string
	err = s.db.QueryRowContext(ctx, `
	SELECT task_id, id, owner_hash, api_key_id, api_key_ref, site_url, gateway_url, status, prompt, model, parameters_json, assets_downloaded, created_at, updated_at
FROM image_tasks WHERE task_id = ? AND owner_hash = ?`, strings.TrimSpace(taskID), ownerHash).Scan(
		&record.TaskID, &record.ID, &record.OwnerHash, &record.APIKeyID, &record.APIKeyRef, &record.SiteURL, &record.GatewayURL, &record.Status,
		&record.Prompt, &record.Model, &record.ParametersJSON, &record.AssetsDownloaded, &createdRaw, &updatedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	if err != nil {
		return TaskRecord{}, fmt.Errorf("read image task checkpoint: %w", err)
	}
	record.CreatedAt = parseTaskTime(createdRaw)
	record.UpdatedAt = parseTaskTime(updatedRaw)
	return record, nil
}

func (s *SQLiteTaskStore) List(ctx context.Context) (result []TaskRecord, resultErr error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite task store is unavailable")
	}
	if err := s.hardenSQLiteFiles(); err != nil {
		return nil, err
	}
	defer func() {
		if hardenErr := s.hardenSQLiteFiles(); resultErr == nil && hardenErr != nil {
			resultErr = hardenErr
		}
	}()
	rows, err := s.db.QueryContext(ctx, `
	SELECT task_id, id, owner_hash, api_key_id, api_key_ref, site_url, gateway_url, status, prompt, model, parameters_json, assets_downloaded, created_at, updated_at
FROM image_tasks ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list image task checkpoints: %w", err)
	}
	defer rows.Close()
	result = make([]TaskRecord, 0)
	for rows.Next() {
		var record TaskRecord
		var createdRaw, updatedRaw string
		if err := rows.Scan(&record.TaskID, &record.ID, &record.OwnerHash, &record.APIKeyID, &record.APIKeyRef, &record.SiteURL, &record.GatewayURL, &record.Status,
			&record.Prompt, &record.Model, &record.ParametersJSON, &record.AssetsDownloaded, &createdRaw, &updatedRaw); err != nil {
			return nil, fmt.Errorf("scan image task checkpoint: %w", err)
		}
		record.CreatedAt = parseTaskTime(createdRaw)
		record.UpdatedAt = parseTaskTime(updatedRaw)
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read image task checkpoints: %w", err)
	}
	// The SQL ordering is authoritative; this secondary sort makes behavior
	// deterministic for timestamps with equal precision across SQLite builds.
	sort.SliceStable(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

// ListForOwner excludes unowned legacy rows and every row belonging to another
// account. Ordering matches the legacy List method.
func (s *SQLiteTaskStore) ListForOwner(ctx context.Context, ownerHash string) (result []TaskRecord, resultErr error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite task store is unavailable")
	}
	if err := s.hardenSQLiteFiles(); err != nil {
		return nil, err
	}
	defer func() {
		if hardenErr := s.hardenSQLiteFiles(); resultErr == nil && hardenErr != nil {
			resultErr = hardenErr
		}
	}()
	rows, err := s.db.QueryContext(ctx, `
	SELECT task_id, id, owner_hash, api_key_id, api_key_ref, site_url, gateway_url, status, prompt, model, parameters_json, assets_downloaded, created_at, updated_at
FROM image_tasks WHERE owner_hash = ? ORDER BY updated_at DESC`, ownerHash)
	if err != nil {
		return nil, fmt.Errorf("list owned image task checkpoints: %w", err)
	}
	defer rows.Close()
	result = make([]TaskRecord, 0)
	for rows.Next() {
		var record TaskRecord
		var createdRaw, updatedRaw string
		if err := rows.Scan(&record.TaskID, &record.ID, &record.OwnerHash, &record.APIKeyID, &record.APIKeyRef, &record.SiteURL, &record.GatewayURL, &record.Status,
			&record.Prompt, &record.Model, &record.ParametersJSON, &record.AssetsDownloaded, &createdRaw, &updatedRaw); err != nil {
			return nil, fmt.Errorf("scan owned image task checkpoint: %w", err)
		}
		record.CreatedAt = parseTaskTime(createdRaw)
		record.UpdatedAt = parseTaskTime(updatedRaw)
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read owned image task checkpoints: %w", err)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func (s *SQLiteTaskStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// hardenSQLiteFiles tightens permissions on the database and WAL/SHM
// sidecars. SQLite may create sidecars lazily, so callers invoke this after
// initialization and each operation that can open a write transaction.
func (s *SQLiteTaskStore) hardenSQLiteFiles() error {
	if s == nil || s.db == nil || s.path == "" {
		return errors.New("sqlite task store is unavailable")
	}
	return hardenSQLitePath(s.path)
}

func hardenSQLitePath(path string) error {
	if err := ensureSQLiteDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, statErr := os.Lstat(candidate)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("%w: inspect %s: %v", ErrUnsafeSQLitePath, candidate, statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is not a regular file", ErrUnsafeSQLitePath, candidate)
		}
		if chmodErr := os.Chmod(candidate, 0o600); chmodErr != nil {
			return fmt.Errorf("secure sqlite task file %s: %w", candidate, chmodErr)
		}
	}
	return nil
}

// ensureSQLiteDirectory creates missing path components one at a time after
// validating the nearest existing ancestor. This avoids MkdirAll silently
// following an attacker-controlled symlink at the application-data boundary.
func ensureSQLiteDirectory(path string) error {
	path = filepath.Clean(path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absPath = normalizeSQLiteSystemPath(absPath)
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
		if errors.Is(statErr, fs.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return fmt.Errorf("%w: inspect sqlite directory %s: %v", ErrUnsafeSQLitePath, current, statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is not a regular directory", ErrUnsafeSQLitePath, current)
		}
	}
	return nil
}

func normalizeSQLiteSystemPath(path string) string {
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

func parseTaskTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}
