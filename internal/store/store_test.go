package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FUTURE AI: store tests use a temp dir per test — never the real data dir.

func TestTokenRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	if got := s.GetToken(); got != "" {
		t.Fatalf("fresh store should have no token, got %q", got)
	}
	if err := s.SetToken("tok-abc"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetToken(); got != "tok-abc" {
		t.Fatalf("token round-trip failed: %q", got)
	}
	if err := s.ClearToken(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetToken(); got != "" {
		t.Fatalf("cleared token should read empty, got %q", got)
	}
}

func TestSecretRoundTripAndEmptyClears(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.SetSecret("tmdb:api-key", "sekrit"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSecret("tmdb:api-key"); got != "sekrit" {
		t.Fatalf("secret round-trip failed: %q", got)
	}
	// Electron filename scheme: ':' becomes '-'.
	want := filepath.Join(dir, "secret-tmdb-api-key.enc.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected secret file at Electron-compatible name %s: %v", want, err)
	}
	// Plaintext must not appear in the file bytes.
	raw, _ := os.ReadFile(want)
	if strings.Contains(string(raw), "sekrit") {
		t.Fatal("secret stored in plaintext")
	}
	// Empty value = clear (Electron behavior).
	if err := s.SetSecret("tmdb:api-key", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSecret("tmdb:api-key"); got != "" {
		t.Fatalf("empty set should clear, got %q", got)
	}
}

func TestSettingsMerge(t *testing.T) {
	s := New(t.TempDir())
	if err := s.SetSettings(map[string]any{"clientId": "pcm-x", "defaultMachine": "m1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(map[string]any{"defaultMachine": "m2"}); err != nil {
		t.Fatal(err)
	}
	got := s.GetSettings()
	if got["clientId"] != "pcm-x" || got["defaultMachine"] != "m2" {
		t.Fatalf("settings merge wrong: %v", got)
	}
	if s.SettingString("clientId") != "pcm-x" {
		t.Fatal("SettingString mismatch")
	}
}
