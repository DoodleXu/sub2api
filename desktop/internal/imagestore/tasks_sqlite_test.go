package imagestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteTaskStorePersistsAndUpdatesCheckpoint(t *testing.T) {
	store, err := NewSQLiteTaskStore(filepath.Join(t.TempDir(), "nested", "tasks.sqlite3"))
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, store.Close()) })
	created := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	requireNoError(t, store.Put(context.Background(), TaskRecord{
		ID: "img-1", TaskID: "task-1", APIKeyID: 42, APIKeyRef: "key-ref", SiteURL: "https://ai.clol.site",
		GatewayURL: "https://ai.clol.site", Status: "processing", Prompt: "a lighthouse", Model: "gpt-image-2",
		ParametersJSON: `{"n":1}`, CreatedAt: created,
	}))
	record, err := store.Get(context.Background(), "task-1")
	requireNoError(t, err)
	if record.Prompt != "a lighthouse" || record.ParametersJSON != `{"n":1}` || record.APIKeyID != 42 {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.CreatedAt.Sub(created) > time.Millisecond || created.Sub(record.CreatedAt) > time.Millisecond {
		t.Fatalf("created time changed: got %s want %s", record.CreatedAt, created)
	}

	requireNoError(t, store.Put(context.Background(), TaskRecord{
		ID: "img-1", TaskID: "task-1", APIKeyID: 42, APIKeyRef: "key-ref", SiteURL: "https://ai.clol.site",
		GatewayURL: "https://ai.clol.site", Status: "completed", Prompt: "a lighthouse", Model: "gpt-image-2",
		ParametersJSON: `{"n":1}`, AssetsDownloaded: true, CreatedAt: created,
	}))
	updated, err := store.Get(context.Background(), "task-1")
	requireNoError(t, err)
	if updated.Status != "completed" {
		t.Fatalf("status = %q", updated.Status)
	}
	if !updated.AssetsDownloaded {
		t.Fatal("assets downloaded marker was not persisted")
	}
	if updated.CreatedAt.Sub(created) > time.Millisecond || created.Sub(updated.CreatedAt) > time.Millisecond {
		t.Fatalf("created time changed after update: got %s want %s", updated.CreatedAt, created)
	}
	items, err := store.List(context.Background())
	requireNoError(t, err)
	if len(items) != 1 {
		t.Fatalf("item count = %d", len(items))
	}
}

func TestTaskOwnerHashIsDomainSeparatedAndOpaque(t *testing.T) {
	first := OwnerHashForSubject("user:42")
	if first == "" || len(first) != 64 {
		t.Fatalf("owner hash = %q, want a 64-character digest", first)
	}
	if first != OwnerHashForSubject(" user:42 ") {
		t.Fatal("owner hash should canonicalize surrounding whitespace")
	}
	if first == OwnerHashForSubject("user:43") {
		t.Fatal("different subjects produced the same owner hash")
	}
	if OwnerHashForSubject("") != "" {
		t.Fatal("blank subject must not produce a shared owner")
	}
	if err := ValidateOwnerHash(first); err != nil {
		t.Fatalf("generated owner hash failed validation: %v", err)
	}
	if err := ValidateOwnerHash("user:42"); !errors.Is(err, ErrInvalidTaskOwner) {
		t.Fatalf("raw subject validation error = %v", err)
	}
}

func TestSQLiteTaskStoreScopesTasksByOwnerAndDoesNotAdoptLegacyRows(t *testing.T) {
	store, err := NewSQLiteTaskStore(filepath.Join(t.TempDir(), "tasks.sqlite3"))
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, store.Close()) })
	ctx := context.Background()
	ownerA := OwnerHashForSubject("user:100")
	ownerB := OwnerHashForSubject("user:200")

	// This row represents a checkpoint imported from a pre-ownership build.
	requireNoError(t, store.Put(ctx, TaskRecord{TaskID: "legacy", ID: "legacy", Status: "processing"}))
	requireNoError(t, store.PutForOwner(ctx, ownerA, TaskRecord{TaskID: "task-a", ID: "task-a", Status: "processing"}))
	requireNoError(t, store.PutForOwner(ctx, ownerB, TaskRecord{TaskID: "task-b", ID: "task-b", Status: "completed"}))

	ownedA, err := store.ListForOwner(ctx, ownerA)
	requireNoError(t, err)
	if len(ownedA) != 1 || ownedA[0].TaskID != "task-a" || ownedA[0].OwnerHash != ownerA {
		t.Fatalf("owner A list = %+v", ownedA)
	}
	ownedB, err := store.ListForOwner(ctx, ownerB)
	requireNoError(t, err)
	if len(ownedB) != 1 || ownedB[0].TaskID != "task-b" {
		t.Fatalf("owner B list = %+v", ownedB)
	}
	if _, err := store.GetForOwner(ctx, ownerB, "task-a"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-owner get error = %v, want not found", err)
	}
	if _, err := store.GetForOwner(ctx, ownerA, "legacy"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("legacy row was adopted by owner A: %v", err)
	}
	if err := store.PutForOwner(ctx, ownerA, TaskRecord{TaskID: "legacy", ID: "legacy", Status: "completed"}); !errors.Is(err, ErrTaskOwnerMismatch) {
		t.Fatalf("legacy adoption error = %v, want owner mismatch", err)
	}
	if err := store.PutForOwner(ctx, ownerB, TaskRecord{TaskID: "task-a", ID: "task-a", Status: "completed"}); !errors.Is(err, ErrTaskOwnerMismatch) {
		t.Fatalf("cross-owner overwrite error = %v, want owner mismatch", err)
	}
	// A same-owner checkpoint can be updated normally.
	requireNoError(t, store.PutForOwner(ctx, ownerA, TaskRecord{TaskID: "task-a", ID: "task-a", Status: "completed"}))
	record, err := store.GetForOwner(ctx, ownerA, "task-a")
	requireNoError(t, err)
	if record.Status != "completed" {
		t.Fatalf("same-owner update status = %q", record.Status)
	}
}

func TestJSONTaskStoreScopesTasksByOwnerAndLeavesLegacyRowsUnowned(t *testing.T) {
	store, err := NewJSONTaskStore(filepath.Join(t.TempDir(), "tasks.json"))
	requireNoError(t, err)
	ctx := context.Background()
	owner := OwnerHashForSubject("user:json")
	requireNoError(t, store.Put(ctx, TaskRecord{TaskID: "legacy", ID: "legacy", Status: "processing"}))
	requireNoError(t, store.PutForOwner(ctx, owner, TaskRecord{TaskID: "owned", ID: "owned", Status: "processing"}))
	items, err := store.ListForOwner(ctx, owner)
	requireNoError(t, err)
	if len(items) != 1 || items[0].TaskID != "owned" {
		t.Fatalf("owned JSON list = %+v", items)
	}
	if _, err := store.GetForOwner(ctx, owner, "legacy"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("legacy JSON row was adopted: %v", err)
	}
}

func TestSQLiteTaskStoreMigratesLegacyJSONWithoutOverwritingNewerRow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.sqlite3")
	legacyPath := filepath.Join(dir, "image-tasks.json")
	legacy := []TaskRecord{{TaskID: "legacy", ID: "legacy", Status: "processing", Prompt: "old"}}
	raw, err := json.Marshal(legacy)
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(legacyPath, raw, 0o600))
	store, err := NewSQLiteTaskStore(dbPath)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, store.Close()) })
	requireNoError(t, store.Put(context.Background(), TaskRecord{TaskID: "legacy", ID: "legacy", Status: "completed", Prompt: "new"}))
	requireNoError(t, store.MigrateJSON(context.Background(), legacyPath))
	record, err := store.Get(context.Background(), "legacy")
	requireNoError(t, err)
	if record.Prompt != "new" {
		t.Fatalf("prompt = %q", record.Prompt)
	}
	_, err = store.Get(context.Background(), "missing")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing task should not expose filesystem error")
	}
}

func TestSQLiteTaskStoreAddsAPIKeyIDToExistingSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	requireNoError(t, err)
	_, err = db.Exec(`CREATE TABLE image_tasks (
		task_id TEXT PRIMARY KEY,
		id TEXT NOT NULL,
		api_key_ref TEXT NOT NULL,
		site_url TEXT NOT NULL,
		gateway_url TEXT NOT NULL,
		status TEXT NOT NULL,
		prompt TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		parameters_json TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	requireNoError(t, err)
	requireNoError(t, db.Close())

	store, err := NewSQLiteTaskStore(dbPath)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, store.Close()) })
	requireNoError(t, store.Put(context.Background(), TaskRecord{TaskID: "task-legacy", ID: "task-legacy", APIKeyID: 77, APIKeyRef: "key-ref", Status: "processing"}))
	record, err := store.Get(context.Background(), "task-legacy")
	requireNoError(t, err)
	if record.APIKeyID != 77 {
		t.Fatalf("api key id = %d, want 77", record.APIKeyID)
	}
}

func TestSQLiteTaskStoreSecuresDatabaseAndSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.sqlite3")
	store, err := NewSQLiteTaskStore(dbPath)
	requireNoError(t, err)
	requireNoError(t, store.Put(context.Background(), TaskRecord{TaskID: "task-1", ID: "task-1", Status: "processing"}))

	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue // SQLite may checkpoint a sidecar before the assertion.
		}
		requireNoError(t, statErr)
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			t.Fatalf("sqlite sidecar is not regular: %s (%v)", path, info.Mode())
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("sqlite file %s permissions = %o, want 600", path, got)
		}
	}

	// Reopening an existing database must tighten a deliberately broadened
	// mode before any SQL is executed.
	requireNoError(t, os.Chmod(dbPath, 0o644))
	requireNoError(t, store.Close())
	reopened, err := NewSQLiteTaskStore(dbPath)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, reopened.Close()) })
	if mode := mustLstat(t, dbPath).Mode().Perm(); mode != 0o600 {
		t.Fatalf("reopened sqlite mode = %o, want 600", mode)
	}
}

func TestSQLiteTaskStoreRejectsSymlinkDatabaseParentAndSidecar(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir, dbPath string)
	}{
		{
			name: "parent symlink",
			setup: func(t *testing.T, dir, dbPath string) {
				outside := filepath.Join(dir, "outside")
				requireNoError(t, os.Mkdir(outside, 0o700))
				requireNoError(t, os.Symlink(outside, filepath.Join(dir, "link")))
			},
		},
		{
			name: "database symlink",
			setup: func(t *testing.T, dir, dbPath string) {
				outside := filepath.Join(dir, "outside.sqlite3")
				requireNoError(t, os.WriteFile(outside, []byte("not-db"), 0o600))
				requireNoError(t, os.Symlink(outside, dbPath))
			},
		},
		{
			name: "wal symlink",
			setup: func(t *testing.T, dir, dbPath string) {
				outside := filepath.Join(dir, "outside-wal")
				requireNoError(t, os.WriteFile(outside, []byte("private"), 0o600))
				requireNoError(t, os.Symlink(outside, dbPath+"-wal"))
			},
		},
		{
			name: "shm symlink",
			setup: func(t *testing.T, dir, dbPath string) {
				outside := filepath.Join(dir, "outside-shm")
				requireNoError(t, os.WriteFile(outside, []byte("private"), 0o600))
				requireNoError(t, os.Symlink(outside, dbPath+"-shm"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "link", "tasks.sqlite3")
			if test.name != "parent symlink" {
				dbPath = filepath.Join(dir, "tasks.sqlite3")
			}
			test.setup(t, dir, dbPath)
			_, err := NewSQLiteTaskStore(dbPath)
			if err == nil || !errors.Is(err, ErrUnsafeSQLitePath) {
				t.Fatalf("expected ErrUnsafeSQLitePath, got %v", err)
			}
		})
	}
}

func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	requireNoError(t, err)
	return info
}
