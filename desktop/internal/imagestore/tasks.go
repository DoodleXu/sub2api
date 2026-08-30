package imagestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrTaskNotFound is shared by the JSON migration and SQLite implementation
// so callers can distinguish a missing checkpoint from a storage failure.
var ErrTaskNotFound = errors.New("image task not found")

// ErrTaskOwnerRequired is returned by the scoped task-store methods when a
// caller does not provide an authenticated account partition.  An empty
// owner is intentionally never treated as the current account: that would
// silently adopt checkpoints written by an older build (or by another local
// account).
var ErrTaskOwnerRequired = errors.New("image task owner is required")

// ErrInvalidTaskOwner indicates that an owner value is not one of the
// canonical, opaque hashes emitted by OwnerHashForSubject.
var ErrInvalidTaskOwner = errors.New("image task owner is invalid")

// ErrTaskOwnerMismatch is returned when an existing task id belongs to a
// different owner.  The application normally maps this to a not-found style
// message so task existence is not disclosed across account partitions.
var ErrTaskOwnerMismatch = errors.New("image task belongs to another owner")

const taskOwnerHashDomain = "sub2api-desktop-image-task-owner:v1:\x00"

// OwnerHashForSubject derives the opaque local partition key for one account
// subject.  We deliberately store only a domain-separated SHA-256 digest, not
// a raw user id/email/JWT subject.  A blank subject returns blank so callers
// can fail closed instead of accidentally sharing unowned records.
func OwnerHashForSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(taskOwnerHashDomain + subject))
	return hex.EncodeToString(sum[:])
}

// HashTaskOwner is a descriptive alias kept for callers that use the shorter
// name in integration code.
func HashTaskOwner(subject string) string { return OwnerHashForSubject(subject) }

// ValidateOwnerHash validates and canonicalizes a previously derived owner
// hash.  It is exported so app bindings and alternate stores can apply the
// same fail-closed contract without duplicating hex/length checks.
func ValidateOwnerHash(ownerHash string) error {
	ownerHash = strings.TrimSpace(ownerHash)
	if ownerHash == "" {
		return ErrTaskOwnerRequired
	}
	if len(ownerHash) != sha256.Size*2 {
		return ErrInvalidTaskOwner
	}
	decoded, err := hex.DecodeString(ownerHash)
	if err != nil || len(decoded) != sha256.Size {
		return ErrInvalidTaskOwner
	}
	return nil
}

func normalizeOwnerHash(ownerHash string) (string, error) {
	ownerHash = strings.ToLower(strings.TrimSpace(ownerHash))
	if err := ValidateOwnerHash(ownerHash); err != nil {
		return "", err
	}
	return ownerHash, nil
}

// TaskRecord is the minimal durable checkpoint needed to resume polling with
// the same API key that created an async image task.
type TaskRecord struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	// OwnerHash is the opaque account partition for this checkpoint. It is
	// intentionally not a raw user id. Empty means an old/unowned record and
	// must never be auto-assigned to a newly signed-in account.
	OwnerHash string `json:"owner_hash,omitempty"`
	// APIKeyID is a non-secret server key identifier used to keep task
	// recovery bound to the key that submitted the request. The secret stays
	// in securestore and is never serialized here.
	APIKeyID   int64  `json:"api_key_id,omitempty"`
	APIKeyRef  string `json:"api_key_ref"`
	SiteURL    string `json:"site_url"`
	GatewayURL string `json:"gateway_url"`
	Status     string `json:"status"`
	Prompt     string `json:"prompt,omitempty"`
	Model      string `json:"model,omitempty"`
	// ParametersJSON is an opaque, non-secret copy of server-accepted image
	// options. It lets the UI explain/resume a task without storing credentials.
	ParametersJSON string `json:"parameters_json,omitempty"`
	// AssetsDownloaded is set only after every returned result has been
	// validated and atomically saved in the private image store. It lets startup
	// recovery distinguish a completed task that still needs local persistence.
	AssetsDownloaded bool      `json:"assets_downloaded,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type TaskStore interface {
	Put(ctx context.Context, task TaskRecord) error
	Get(ctx context.Context, taskID string) (TaskRecord, error)
	List(ctx context.Context) ([]TaskRecord, error)
}

// ScopedTaskStore is an additive interface for account-isolated access. The
// legacy TaskStore methods remain available for migration and source
// compatibility, while desktop application code must use these methods for
// all current-account reads and writes.
type ScopedTaskStore interface {
	TaskStore
	PutForOwner(ctx context.Context, ownerHash string, task TaskRecord) error
	GetForOwner(ctx context.Context, ownerHash, taskID string) (TaskRecord, error)
	ListForOwner(ctx context.Context, ownerHash string) ([]TaskRecord, error)
}

// OwnedTaskStore is a compatibility alias for integrations that used the
// owner terminology before ScopedTaskStore was named.
type OwnedTaskStore = ScopedTaskStore

type JSONTaskStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONTaskStore(path string) (*JSONTaskStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("task store path is required")
	}
	return &JSONTaskStore{path: filepath.Clean(path)}, nil
}

func (s *JSONTaskStore) Put(ctx context.Context, task TaskRecord) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(task.TaskID) == "" {
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
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for index := range tasks {
		if tasks[index].TaskID == task.TaskID {
			if tasks[index].OwnerHash != task.OwnerHash {
				return ErrTaskOwnerMismatch
			}
			tasks[index] = task
			replaced = true
			break
		}
	}
	if !replaced {
		tasks = append(tasks, task)
	}
	return s.saveLocked(tasks)
}

// PutForOwner writes a checkpoint only into the supplied account partition.
// In particular, an existing unowned legacy row is not adopted.
func (s *JSONTaskStore) PutForOwner(ctx context.Context, ownerHash string, task TaskRecord) error {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return err
	}
	if task.OwnerHash != "" {
		existingOwner, ownerErr := normalizeOwnerHash(task.OwnerHash)
		if ownerErr != nil {
			return ownerErr
		}
		if existingOwner != ownerHash {
			return ErrTaskOwnerMismatch
		}
	}
	task.OwnerHash = ownerHash
	return s.Put(ctx, task)
}

func (s *JSONTaskStore) Get(ctx context.Context, taskID string) (TaskRecord, error) {
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return TaskRecord{}, err
	}
	for _, task := range tasks {
		if task.TaskID == strings.TrimSpace(taskID) {
			return task, nil
		}
	}
	return TaskRecord{}, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
}

// GetForOwner returns a task only when both id and owner match. A mismatch is
// deliberately reported as ErrTaskNotFound to avoid cross-account existence
// disclosure; PutForOwner still uses ErrTaskOwnerMismatch for diagnostics.
func (s *JSONTaskStore) GetForOwner(ctx context.Context, ownerHash, taskID string) (TaskRecord, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return TaskRecord{}, err
	}
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return TaskRecord{}, err
	}
	for _, task := range tasks {
		if task.TaskID == strings.TrimSpace(taskID) && task.OwnerHash == ownerHash {
			return task, nil
		}
	}
	return TaskRecord{}, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
}

func (s *JSONTaskStore) List(ctx context.Context) ([]TaskRecord, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt) })
	return tasks, nil
}

// ListForOwner excludes every unowned or differently owned checkpoint.
func (s *JSONTaskStore) ListForOwner(ctx context.Context, ownerHash string) ([]TaskRecord, error) {
	ownerHash, err := normalizeOwnerHash(ownerHash)
	if err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	result := make([]TaskRecord, 0, len(tasks))
	for _, task := range tasks {
		if task.OwnerHash == ownerHash {
			result = append(result, task)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func (s *JSONTaskStore) loadLocked() ([]TaskRecord, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return []TaskRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read image tasks: %w", err)
	}
	var tasks []TaskRecord
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("decode image tasks: %w", err)
	}
	return tasks, nil
}

func (s *JSONTaskStore) saveLocked(tasks []TaskRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create image task directory: %w", err)
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("encode image tasks: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".image-tasks-*.tmp")
	if err != nil {
		return fmt.Errorf("create image task temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set image task permissions: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write image tasks: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync image tasks: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close image tasks: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace image tasks: %w", err)
	}
	return nil
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
