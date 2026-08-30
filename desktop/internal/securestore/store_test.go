package securestore

import "testing"

func TestProtectionLevelIsConservative(t *testing.T) {
	if got := ProtectionLevel(NewKeyringStore("test-service")); got != "os" {
		t.Fatalf("keyring protection level = %q, want os", got)
	}
	if got := ProtectionLevel(NewMemoryStore()); got != "software" {
		t.Fatalf("memory protection level = %q, want software", got)
	}
	if got := ProtectionLevel(nil); got != "software" {
		t.Fatalf("nil protection level = %q, want software", got)
	}
}
