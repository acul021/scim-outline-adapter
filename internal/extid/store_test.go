package extid

import (
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extid.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("u1", "ext-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("u2", "ext-2"); err != nil {
		t.Fatal(err)
	}

	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Get("u1"); got != "ext-1" {
		t.Fatalf("Get(u1) = %q after reopen", got)
	}
	if id, ok := re.Lookup("ext-2"); !ok || id != "u2" {
		t.Fatalf("Lookup(ext-2) = %q, %v", id, ok)
	}

	if err := re.Delete("u1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := re.Lookup("ext-1"); ok {
		t.Fatal("ext-1 still mapped after delete")
	}
}

func TestFileStoreStaysOneToOne(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "extid.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Set("u1", "ext-1")

	// Re-keying a user frees its old externalId.
	_ = s.Set("u1", "ext-new")
	if _, ok := s.Lookup("ext-1"); ok {
		t.Fatal("old externalId still mapped")
	}

	// Moving an externalId to another user clears it from the first one.
	_ = s.Set("u2", "ext-new")
	if got := s.Get("u1"); got != "" {
		t.Fatalf("u1 kept externalId %q", got)
	}
	if id, _ := s.Lookup("ext-new"); id != "u2" {
		t.Fatalf("Lookup(ext-new) = %q, want u2", id)
	}
}
