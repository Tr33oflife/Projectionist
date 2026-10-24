// Package store persists Plex Collection Manager's settings and secrets.
//
// FUTURE AI — this is the Go port of the Electron app's src/token-store.js.
// Same on-disk layout, same rules:
//
//   - settings.json: PLAIN JSON, not secret (clientId, defaultMachine,
//     AI base URL/model, advisor settings live elsewhere). Merge-on-write,
//     byte-format identical to the Electron app's file, in the SAME data
//     directory (%APPDATA%\Plex Collection Manager on Windows), so an
//     existing install's settings carry over to the Go build.
//   - The plex.tv USER token and the named API-key secrets are NEVER
//     stored in plain JSON and NEVER leave the server process. The web UI
//     only ever sees "is it set?" booleans.
//   - Per-server accessTokens are NOT stored — they are re-fetched from
//     plex.tv /api/v2/resources on every launch (same as Electron).
//
// Secret backends (see secret_windows.go / secret_other.go):
//   - Windows: DPAPI (CryptProtectData) blobs in token.enc.json /
//     secret-<name>.enc.json. NOTE: this does NOT read the Electron app's
//     keytar (Credential Manager) entries — after switching to the Go
//     build, the owner pastes his API keys once. settings.json DOES carry over.
//   - Other OS: AES-256-GCM file, byte-compatible with the Electron app's
//     encrypted-file fallback ({iv,tag,data} base64 JSON, key in
//     token.key, mode 0600). Honest best-effort, not DPAPI-grade — same
//     caveat the Electron code carried.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// ServiceName is kept from the Electron app (its keytar service name) so
// the two builds are recognisably the same product's store. Do not rename.
const ServiceName = "plex-collection-manager"

const (
	tokenFile    = "token.enc.json"
	keyFile      = "token.key"
	settingsFile = "settings.json"
)

// Store is a directory-backed settings + secrets store. Safe for
// concurrent use (the HTTP server is multi-goroutine; the Electron app
// was effectively single-threaded here, Go is not).
type Store struct {
	dir string
	mu  sync.Mutex
}

// New opens (creating if needed) the store rooted at dir.
func New(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

// Dir returns the data directory (advisor cache, overlay backups, badge
// folder etc. live under it too — same layout as Electron's userData).
func (s *Store) Dir() string { return s.dir }

// Backend reports which secret backend protects secrets at rest:
// "dpapi" on Windows, "file" elsewhere. The UI displays this verbatim.
func (s *Store) Backend() string { return secretBackend }

// ---- user token -----------------------------------------------------------

// SetToken persists the plex.tv user token.
func (s *Store) SetToken(token string) error {
	if token == "" {
		return errors.New("refusing to store an empty token")
	}
	return s.writeSecretFile(tokenFile, token)
}

// GetToken returns the stored user token, or "" when none is stored (or
// it is unreadable — the Electron app treated that as signed-out too).
func (s *Store) GetToken() string {
	v, err := s.readSecretFile(tokenFile)
	if err != nil {
		return ""
	}
	return v
}

// ClearToken removes the stored user token (logout). The encryption key
// material is deliberately kept — wiping it would orphan the other
// secrets (same note as the Electron store).
func (s *Store) ClearToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir, tokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---- named secrets ----------------------------------------------------------

var secretNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// secretPath mirrors the Electron fallback filename scheme:
// secret-<name with [^a-z0-9_-] (case-insensitive) replaced by '-'>.enc.json
func (s *Store) secretPath(name string) string {
	safe := secretNameSanitizer.ReplaceAllString(name, "-")
	return filepath.Join(s.dir, "secret-"+safe+".enc.json")
}

// SetSecret stores a named secret ('tmdb:api-key', 'trakt:oauth', ...).
// An empty value CLEARS the secret (Electron behavior — Settings uses it).
func (s *Store) SetSecret(name, value string) error {
	if name == "" {
		return errors.New("setSecret requires a name")
	}
	if value == "" {
		return s.ClearSecret(name)
	}
	return s.writeSecretFile(filepath.Base(s.secretPath(name)), value)
}

// GetSecret returns the named secret, or "" when unset/unreadable.
func (s *Store) GetSecret(name string) string {
	v, err := s.readSecretFile(filepath.Base(s.secretPath(name)))
	if err != nil {
		return ""
	}
	return v
}

// ClearSecret removes a named secret.
func (s *Store) ClearSecret(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.secretPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---- settings (plain JSON, not secret) -------------------------------------

// GetSettings returns the whole settings object ({} when none stored).
func (s *Store) GetSettings() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getSettingsLocked()
}

func (s *Store) getSettingsLocked() map[string]any {
	raw, err := os.ReadFile(filepath.Join(s.dir, settingsFile))
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// SetSettings merges patch over the stored settings (Electron semantics:
// setSettings({...prev, ...obj})), written as indented JSON.
func (s *Store) SetSettings(patch map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.getSettingsLocked()
	for k, v := range patch {
		cur[k] = v
	}
	return s.writeSettingsLocked(cur)
}

// SettingString is a convenience accessor for the string-valued settings
// (clientId, defaultMachine, aiBaseUrl, aiModel).
func (s *Store) SettingString(key string) string {
	v, _ := s.GetSettings()[key].(string)
	return v
}

// ---- secret file plumbing (crypto lives in the platform files) --------------

func (s *Store) writeSecretFile(fileName, plaintext string) error {
	packed, err := encryptSecret(plaintext, s.dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, fileName), []byte(packed), 0o600)
}

func (s *Store) readSecretFile(fileName string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, fileName))
	if err != nil {
		return "", err
	}
	return decryptSecret(string(raw), s.dir)
}
