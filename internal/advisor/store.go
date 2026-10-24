// Package advisor is the Go port of the Electron app's Advisor engine
// (v0.4 src/advisor/{store,scan,dubscan,suggest}.js). It is a FAITHFUL
// port: same scan phases, same cache TTLs, same generator logic and
// thresholds, same card shapes and JSON field names (the reused web
// frontend consumes scan + cards verbatim).
//
// Safety (AGENTS.md): this package never deletes Plex media, files,
// sections or collections, never calls destructive Plex endpoints, and
// never operates a real Plex account in tests — fixtures only.
package advisor

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// store.go — port of src/advisor/store.js
// ---------------------------------------------------------------------------

// DayMS is one day in milliseconds (JS DAY_MS).
const DayMS = 24 * 60 * 60 * 1000

// TTLMS is the per-namespace cache TTL map, EXACTLY as store.js TTL_MS:
//
//	tmdb 30d, tmdb-lists 24h, omdb 30d, anilist 30d, trakt 24h, dub 90d.
//
// Detail endpoints use 'tmdb' (30d); list endpoints use 'tmdb-lists' (24h).
// Scan snapshots + dismissals + drafts have NO TTL (the owner's data).
var TTLMS = map[string]int64{
	"tmdb":       30 * DayMS,
	"tmdb-lists": DayMS,
	"omdb":       30 * DayMS,
	"anilist":    30 * DayMS,
	"trakt":      DayMS,
	"dub":        90 * DayMS,
}

// IsFresh is the pure TTL check from store.js (exported for tests).
// fetchedAt/now are milliseconds since the epoch.
func IsFresh(fetchedAt, ttlMs, now int64) bool {
	// JS: if (!Number.isFinite(fetchedAt)) return false;
	// Go int64 cannot be NaN/Inf; the JS non-finite case arrives here only
	// via a missing field, which decodes as 0 and is stale for any now > ttl.
	if fetchedAt == math.MinInt64 {
		return false
	}
	return now-fetchedAt < ttlMs
}

var unsafeKeyChars = regexp.MustCompile(`[^a-zA-Z0-9_.\-]`)
var leadingDots = regexp.MustCompile(`^\.+`)

// safeKey sanitizes a cache key segment into a safe relative path
// (no .., no separators), exactly as store.js safeKey.
func safeKey(key string) string {
	s := unsafeKeyChars.ReplaceAllString(key, "-")
	s = leadingDots.ReplaceAllString(s, "")
	if len(s) > 120 {
		s = s[:120]
	}
	if s == "" {
		return "key"
	}
	return s
}

// Settings mirrors DEFAULT_SETTINGS in store.js. JSON tags are the JS
// keys EXACTLY.
type Settings struct {
	GenreThreshold    int             `json:"genreThreshold"`
	DecadeThreshold   int             `json:"decadeThreshold"`
	DirectorThreshold int             `json:"directorThreshold"`
	StudioThreshold   int             `json:"studioThreshold"`
	ActorThreshold    int             `json:"actorThreshold"`
	HiddenGemRating   float64         `json:"hiddenGemRating"`
	AnimeSections     []string        `json:"animeSections"`
	AnimeOverrides    map[string]bool `json:"animeOverrides"`
	IMDBBadges        bool            `json:"imdbBadges"`
}

// DefaultSettings returns DEFAULT_SETTINGS from store.js.
func DefaultSettings() Settings {
	return Settings{
		GenreThreshold:    8,
		DecadeThreshold:   8,
		DirectorThreshold: 4,
		StudioThreshold:   4, // v0.4 — min titles for a Plex-native studio suggestion
		ActorThreshold:    3,
		HiddenGemRating:   7.5,
		AnimeSections:     []string{},
		AnimeOverrides:    map[string]bool{},
		IMDBBadges:        true, // run the OMDb pass during scans
	}
}

// Draft is the stored draft shape from store.js saveDraft: the draft
// fields the Builder uses, plus the generated id and savedAt timestamp
// the store stamps on save.
type Draft struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	MemberRatingKeys []string `json:"memberRatingKeys"`
	SectionKey       string   `json:"sectionKey"`
	Type             string   `json:"type"`
	Reason           string   `json:"reason"`
	SavedAt          string   `json:"savedAt"`
}

// CacheStats is the cacheSize() result from store.js: { bytes, files }.
type CacheStats struct {
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

// Store is the Advisor's disk: JSON cache, scan snapshots, dismissals,
// drafts and settings under <dir>/advisor-cache/ (port of
// createAdvisorStore(userDataDir)). Atomic writes (tmp + rename),
// per-namespace TTLs, no network, no Plex token — pure disk I/O.
type Store struct {
	dir       string // base dir passed to NewStore (JS userDataDir)
	cacheDir  string // <dir>/advisor-cache
	storeFile string // <cacheDir>/advisor-store.json
	mu        sync.Mutex
}

// NewStore opens the advisor store rooted at dir (the data dir; the JS
// cache lives in its advisor-cache/ subdirectory).
func NewStore(dir string) *Store {
	cacheDir := filepath.Join(dir, "advisor-cache")
	return &Store{
		dir:       dir,
		cacheDir:  cacheDir,
		storeFile: filepath.Join(cacheDir, "advisor-store.json"),
	}
}

// CacheDir returns the advisor-cache directory.
func (s *Store) CacheDir() string { return s.cacheDir }

func (s *Store) cachePath(ns, key string) string {
	segs := []string{}
	for _, seg := range strings.Split(key, "/") {
		seg = safeKey(seg)
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	parts := append([]string{s.cacheDir, safeKey(ns)}, segs...)
	return filepath.Join(parts...) + ".json"
}

func atomicWrite(filePath string, data any) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	tmp := filePath + ".tmp-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filePath)
}

func readJSONFile(filePath string) json.RawMessage {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	// JS readJson returns null on unparseable content.
	if !json.Valid(raw) {
		return nil
	}
	return raw
}

// cacheEntry is the on-disk wrapper for namespaced cache values:
// { fetchedAt: <ms epoch>, data: <value> }.
type cacheEntry struct {
	FetchedAt int64           `json:"fetchedAt"`
	Data      json.RawMessage `json:"data"`
}

// Get implements meta.Cache: it returns the cached value for ns/key when
// present and fresh under that namespace's TTL.
//
// JS store.get throws on an unknown namespace. The meta.Cache interface
// has no error return, so the Go adaptation reports an unknown namespace
// (and any unreadable entry) as a miss — see the advisor port notes.
func (s *Store) Get(ns, key string) (json.RawMessage, bool) {
	ttl, ok := TTLMS[ns]
	if !ok {
		return nil, false
	}
	raw := readJSONFile(s.cachePath(ns, key))
	if raw == nil {
		return nil, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, false
	}
	if !IsFresh(entry.FetchedAt, ttl, time.Now().UnixMilli()) {
		return nil, false
	}
	return entry.Data, true
}

// Set implements meta.Cache: it stores data under ns/key, stamped with
// the current time for TTL freshness. Unknown namespaces are a no-op
// (see Get for why). Write errors are dropped by the interface; the
// store's own callers use the internal setEntry when they need the error.
func (s *Store) Set(ns, key string, data json.RawMessage) {
	_ = s.setEntry(ns, key, data)
}

func (s *Store) setEntry(ns, key string, data json.RawMessage) error {
	if _, ok := TTLMS[ns]; !ok {
		return fmt.Errorf("unknown advisor cache namespace: %s", ns)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.cachePath(ns, key), cacheEntry{
		FetchedAt: time.Now().UnixMilli(),
		Data:      data,
	})
}

// -- scan snapshots (no TTL — last full scan per server) --------------------

// GetScan returns the last persisted scan for a server, or (nil, nil)
// when there is none. JS: entry ? entry.data : null.
func (s *Store) GetScan(machineID string) (*Scan, error) {
	raw := readJSONFile(s.cachePath("scan", machineID))
	if raw == nil {
		return nil, nil
	}
	var entry struct {
		ScannedAt epochMs         `json:"scannedAt"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || entry.Data == nil {
		return nil, nil
	}
	var scan Scan
	if err := json.Unmarshal(entry.Data, &scan); err != nil {
		return nil, err
	}
	return &scan, nil
}

// SetScan persists a scan snapshot (no TTL wrapper semantics — stored as
// { scannedAt: <ms epoch>, data: scan } exactly as store.js).
func (s *Store) SetScan(machineID string, scan *Scan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.cachePath("scan", machineID), map[string]any{
		"scannedAt": time.Now().UnixMilli(),
		"data":      scan,
	})
}

// -- advisor-store.json: dismissals, drafts, settings (NOT cache) -----------

// readStoreMap returns advisor-store.json as a raw field map, preserving
// any unknown top-level fields across writes (JS readStore behavior:
// the parsed object, or { dismissals: [], drafts: [], settings: {} }).
func (s *Store) readStoreMap() map[string]json.RawMessage {
	out := map[string]json.RawMessage{
		"dismissals": json.RawMessage("[]"),
		"drafts":     json.RawMessage("[]"),
		"settings":   json.RawMessage("{}"),
	}
	raw := readJSONFile(s.storeFile)
	if raw == nil {
		return out
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out
	}
	for k, v := range parsed {
		out[k] = v
	}
	return out
}

func (s *Store) writeStoreMap(m map[string]json.RawMessage) error {
	return atomicWrite(s.storeFile, m)
}

func decodeStringList(raw json.RawMessage) []string {
	out := []string{}
	if raw == nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func decodeDrafts(raw json.RawMessage) []Draft {
	out := []Draft{}
	if raw == nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []Draft{}
	}
	return out
}

// GetDismissals returns the dismissed card ids (stable suggestion ids).
func (s *Store) GetDismissals() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return decodeStringList(s.readStoreMap()["dismissals"]), nil
}

// Dismiss records a card id as dismissed (no duplicates).
func (s *Store) Dismiss(cardID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readStoreMap()
	list := decodeStringList(m["dismissals"])
	for _, d := range list {
		if d == cardID {
			return s.writeStoreMap(m) // JS still writes; content unchanged
		}
	}
	list = append(list, cardID)
	raw, _ := json.Marshal(list)
	m["dismissals"] = raw
	return s.writeStoreMap(m)
}

// Undismiss removes a card id from the dismissals.
func (s *Store) Undismiss(cardID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readStoreMap()
	list := decodeStringList(m["dismissals"])
	out := make([]string, 0, len(list))
	for _, d := range list {
		if d != cardID {
			out = append(out, d)
		}
	}
	raw, _ := json.Marshal(out)
	m["dismissals"] = raw
	return s.writeStoreMap(m)
}

// ResetDismissals clears all dismissals.
func (s *Store) ResetDismissals() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readStoreMap()
	m["dismissals"] = json.RawMessage("[]")
	return s.writeStoreMap(m)
}

// GetDrafts returns the saved collection drafts.
func (s *Store) GetDrafts() ([]Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return decodeDrafts(s.readStoreMap()["drafts"]), nil
}

// SaveDraft saves a draft, generating its id (`draft-<ms epoch>`) when
// absent and stamping savedAt (ISO-8601 UTC), mirroring store.js. An
// existing draft with the same id is replaced in place.
func (s *Store) SaveDraft(draft Draft) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readStoreMap()
	id := draft.ID
	if id == "" {
		id = fmt.Sprintf("draft-%d", time.Now().UnixMilli())
	}
	rec := draft
	rec.ID = id
	rec.SavedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	drafts := decodeDrafts(m["drafts"])
	found := false
	for i, d := range drafts {
		if d.ID == id {
			drafts[i] = rec
			found = true
			break
		}
	}
	if !found {
		drafts = append(drafts, rec)
	}
	raw, _ := json.Marshal(drafts)
	m["drafts"] = raw
	if err := s.writeStoreMap(m); err != nil {
		return Draft{}, err
	}
	return rec, nil
}

// DeleteDraft removes a draft by id.
func (s *Store) DeleteDraft(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readStoreMap()
	drafts := decodeDrafts(m["drafts"])
	out := make([]Draft, 0, len(drafts))
	for _, d := range drafts {
		if d.ID != id {
			out = append(out, d)
		}
	}
	raw, _ := json.Marshal(out)
	m["drafts"] = raw
	return s.writeStoreMap(m)
}

// settingsMap returns a settings object as a raw map: defaults merged
// under the stored settings (JS { ...DEFAULT_SETTINGS, ...stored }).
func settingsMergedRaw(stored json.RawMessage) map[string]json.RawMessage {
	defaultsRaw, _ := json.Marshal(DefaultSettings())
	merged := map[string]json.RawMessage{}
	_ = json.Unmarshal(defaultsRaw, &merged)
	if stored != nil {
		var storedMap map[string]json.RawMessage
		if err := json.Unmarshal(stored, &storedMap); err == nil {
			for k, v := range storedMap {
				merged[k] = v
			}
		}
	}
	return merged
}

func decodeSettings(merged map[string]json.RawMessage) Settings {
	raw, _ := json.Marshal(merged)
	out := DefaultSettings()
	_ = json.Unmarshal(raw, &out)
	// JS spread keeps an explicitly stored empty array/map; a JSON null
	// would decode to nil here — normalize the container fields back to
	// empty (non-nil) so generators never range over a nil map surprise.
	if out.AnimeSections == nil {
		out.AnimeSections = []string{}
	}
	if out.AnimeOverrides == nil {
		out.AnimeOverrides = map[string]bool{}
	}
	return out
}

// GetSettings returns DEFAULT_SETTINGS merged over the stored settings.
func (s *Store) GetSettings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return decodeSettings(settingsMergedRaw(s.readStoreMap()["settings"])), nil
}

// SetSettings merges patch (a partial settings object, as JS receives)
// over defaults + stored settings, persists the merged object, and
// returns it — exactly store.js setSettings.
func (s *Store) SetSettings(patch map[string]any) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readStoreMap()
	merged := settingsMergedRaw(m["settings"])
	for k, v := range patch {
		raw, err := json.Marshal(v)
		if err != nil {
			return Settings{}, err
		}
		merged[k] = raw
	}
	raw, _ := json.Marshal(merged)
	m["settings"] = raw
	if err := s.writeStoreMap(m); err != nil {
		return Settings{}, err
	}
	return decodeSettings(merged), nil
}

// SetSettingsStruct is a typed convenience over SetSettings for callers
// holding a full Settings value: it persists it as the patch object.
func (s *Store) SetSettingsStruct(patch Settings) (Settings, error) {
	raw, err := json.Marshal(patch)
	if err != nil {
		return Settings{}, err
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return Settings{}, err
	}
	return s.SetSettings(asMap)
}

// -- cache maintenance -------------------------------------------------------

// CacheSize walks the cache dir (excluding advisor-store.json) and sums
// the .json bytes, as store.js cacheSize.
func (s *Store) CacheSize() (CacheStats, error) {
	stats := CacheStats{}
	_ = filepath.Walk(s.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // missing dir / vanished file: JS tolerates both
		}
		if info.IsDir() {
			return nil
		}
		if info.Name() != "advisor-store.json" && strings.HasSuffix(info.Name(), ".json") {
			stats.Files++
			stats.Bytes += info.Size()
		}
		return nil
	})
	return stats, nil
}

// ClearCache wipes cached API data but PRESERVES advisor-store.json
// (dismissals, drafts and settings are the owner's choices, not cache).
func (s *Store) ClearCache() error {
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return nil // JS: unreadable cache dir is a silent no-op
	}
	for _, e := range entries {
		if e.Name() == "advisor-store.json" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.cacheDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// sortedKeys is a small helper for deterministic map iteration where JS
// Map/object order is insertion order and Go's is not; call sites that
// need JS order keep an explicit order slice instead. This exists for
// tests/utilities that only need determinism.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
