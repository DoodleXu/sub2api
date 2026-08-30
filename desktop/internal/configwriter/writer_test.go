package configwriter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONWriterUsesPrivateAtomicFileAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "connection.json")
	writer, err := NewJSONWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	first := ConnectionConfig{SiteURL: "https://ai.clol.site", GatewayURL: "https://ai.clol.site", APIKeyRef: "key-ref", Label: "first"}
	if err := writer.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if mode := testFileMode(t, path); mode != 0o600 {
		t.Fatalf("connection metadata mode = %o, want 600", mode)
	}
	second := first
	second.Label = "second"
	if err := writer.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	backupPath := path + ".bak"
	if mode := testFileMode(t, backupPath); mode != 0o600 {
		t.Fatalf("connection backup mode = %o, want 600", mode)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) == "" || !containsJSONField(backup, "label", "first") {
		t.Fatalf("backup did not preserve prior metadata: %s", backup)
	}
}

func TestJSONWriterRejectsSymlinkTargetAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")
	writer, err := NewJSONWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	config := ConnectionConfig{SiteURL: "https://ai.clol.site", GatewayURL: "https://ai.clol.site", APIKeyRef: "key-ref"}

	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := writer.Save(context.Background(), config); err == nil {
		t.Fatal("expected connection symlink rejection")
	}
	assertFileContents(t, victim, "do-not-touch")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := writer.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	backupVictim := filepath.Join(dir, "backup-victim")
	if err := os.WriteFile(backupVictim, []byte("keep-backup-victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backupVictim, path+".bak"); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := writer.Save(context.Background(), config); err == nil {
		t.Fatal("expected backup symlink rejection")
	}
	assertFileContents(t, backupVictim, "keep-backup-victim")
}

func TestJSONWriterRejectsIntermediateParentSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "linked-config")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	_, err := NewJSONWriter(filepath.Join(link, "connection.json"))
	if err == nil || !errors.Is(err, ErrUnsafeConfigPath) {
		t.Fatalf("expected ErrUnsafeConfigPath, got %v", err)
	}
}

func testFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func containsJSONField(data []byte, key, value string) bool {
	needle := `"` + key + `": "` + value + `"`
	return strings.Contains(string(data), needle)
}

func assertFileContents(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("%s was modified: %q", path, data)
	}
}
