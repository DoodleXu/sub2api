// Package securestore defines the boundary used by the desktop app for
// credentials. The default implementation delegates to the operating system
// credential store; an in-memory store is available only when explicitly
// enabled for tests/development.
package securestore

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

var ErrNotFound = errors.New("secure value not found")

// Store keeps sensitive values out of the JSON configuration file.
type Store interface {
	Set(ctx context.Context, name, value string) error
	Get(ctx context.Context, name string) (string, error)
	Delete(ctx context.Context, name string) error
}

const defaultService = "org.sub2api.desktop"

// NewPlatformStore returns the OS-backed store used by production builds.
// The in-memory implementation is available only when explicitly requested
// for local development/CI; it is never selected silently after a keychain
// error because that would make a user believe a credential survived restart.
func NewPlatformStore() Store {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SUB2API_DESKTOP_INSECURE_MEMORY_STORE")), "1") {
		return NewMemoryStore()
	}
	return NewKeyringStore(defaultService)
}

// ProtectionLevel reports the protection boundary of a concrete store. It is
// deliberately conservative: the current go-keyring integration proves that
// a value is kept in the OS credential manager, but it does not prove that a
// hardware-backed Secure Enclave/TPM key was generated. A future native
// implementation can return "hardware" once that guarantee is available.
func ProtectionLevel(store Store) string {
	switch store.(type) {
	case *KeyringStore:
		return "os"
	case *MemoryStore:
		return "software"
	default:
		return "software"
	}
}

// KeyringStore delegates secret storage to the operating system. go-keyring
// uses macOS Keychain, Windows Credential Manager, and Secret Service on
// Linux; the desktop app never has to serialize the underlying secret.
type KeyringStore struct {
	service string
}

func NewKeyringStore(service string) *KeyringStore {
	service = strings.TrimSpace(service)
	if service == "" {
		service = defaultService
	}
	return &KeyringStore{service: service}
}

func (s *KeyringStore) Set(ctx context.Context, name, value string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secure value name is required")
	}
	if value == "" {
		return errors.New("secure value cannot be empty")
	}
	if err := keyring.Set(s.service, name, value); err != nil {
		return err
	}
	return nil
}

func (s *KeyringStore) Get(ctx context.Context, name string) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrNotFound
	}
	value, err := keyring.Get(s.service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *KeyringStore) Delete(ctx context.Context, name string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if err := keyring.Delete(s.service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
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

// MemoryStore is safe for development and tests, but intentionally does not
// persist secrets. This makes the insecure fallback explicit instead of
// silently writing an API key to disk.
type MemoryStore struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[string]string)}
}

func (s *MemoryStore) Set(_ context.Context, name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secure value name is required")
	}
	if value == "" {
		return errors.New("secure value cannot be empty")
	}
	s.mu.Lock()
	s.values[name] = value
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Get(_ context.Context, name string) (string, error) {
	s.mu.RLock()
	value, ok := s.values[strings.TrimSpace(name)]
	s.mu.RUnlock()
	if !ok || value == "" {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	delete(s.values, strings.TrimSpace(name))
	s.mu.Unlock()
	return nil
}

// Mask returns a short, non-reversible hint suitable for UI display and logs.
func Mask(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "••••••••"
	}
	return value[:4] + "••••" + value[len(value)-4:]
}
