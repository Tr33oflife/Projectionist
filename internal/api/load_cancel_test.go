package api

// load_cancel_test.go — pins the getCollectionItems load-cancellation
// contract (v1.20.1):
//
//   - Loads for DIFFERENT collections must never cancel each other.
//     Before the fix, nextLoadCtx was a single global flight: any new
//     load cancelled whatever was in flight, and a cancelled load
//     answers { ok:true, cancelled:true, items:[] } — which the
//     poster generator (the one caller that did not check the flag)
//     rendered as a genuinely empty collection on real, slow servers.
//   - A load for the SAME collection still supersedes a stale one
//     (the detail view's v0.4.4 semantics, unchanged).
//
// The fake PMS blocks in its children endpoint until released, so the
// overlap is deterministic rather than a timing race.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type slowFakePMS struct {
	arrived chan string
	release chan struct{}
	once    sync.Once
}

func newSlowFakePMS(t *testing.T) (*slowFakePMS, *httptest.Server) {
	t.Helper()
	f := &slowFakePMS{arrived: make(chan string, 32), release: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	t.Cleanup(f.releaseAll)
	return f, srv
}

func (f *slowFakePMS) releaseAll() { f.once.Do(func() { close(f.release) }) }

func (f *slowFakePMS) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/library/metadata/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	writeMeta := func(list []any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{
			"size": len(list), "totalSize": len(list), "Metadata": list}})
	}
	if len(parts) == 2 && parts[1] == "children" {
		f.arrived <- id
		select {
		case <-f.release:
		case <-r.Context().Done():
			return
		}
		items := []any{}
		for i := 1; i <= 2; i++ {
			rk := fmt.Sprintf("%s0%d", id, i)
			items = append(items, map[string]any{
				"ratingKey": rk, "type": "movie", "title": "Member " + rk, "year": 2001,
				"thumb": "/library/metadata/" + rk + "/thumb/1",
			})
		}
		writeMeta(items)
		return
	}
	if len(parts) == 1 {
		// The collection record (no smart flag -> metadata children path).
		writeMeta([]any{map[string]any{"ratingKey": id, "type": "collection", "title": "Collection " + id}})
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *slowFakePMS) waitArrival(t *testing.T, key string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case got := <-f.arrived:
			if got == key {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the fake PMS to see a children load for %s", key)
		}
	}
}

type loadResult struct {
	status int
	out    map[string]any
	err    error
}

func fireLoad(ts *httptest.Server, ratingKey string) chan loadResult {
	ch := make(chan loadResult, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/plex/getCollectionItems",
			strings.NewReader(fmt.Sprintf(`{"ratingKey":%q}`, ratingKey)))
		if err != nil {
			ch <- loadResult{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-PCM-Token", testToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- loadResult{err: err}
			return
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil {
			ch <- loadResult{err: fmt.Errorf("response not JSON: %s", raw)}
			return
		}
		ch <- loadResult{status: res.StatusCode, out: out}
	}()
	return ch
}

func awaitLoad(t *testing.T, ch chan loadResult) loadResult {
	t.Helper()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("load request failed: %v", res.err)
		}
		if res.status != 200 || res.out["ok"] != true {
			t.Fatalf("load envelope = %d %v", res.status, res.out)
		}
		return res
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for a getCollectionItems response")
		return loadResult{}
	}
}

func loadItems(t *testing.T, res loadResult) []any {
	t.Helper()
	items, _ := res.out["items"].([]any)
	return items
}

// Two overlapping loads for DIFFERENT collections: both must complete
// with their members. (Pre-fix this fails: the second load cancels the
// first, which answers cancelled+empty.)
func TestGetCollectionItemsCrossKeyLoadsDoNotCancelEachOther(t *testing.T) {
	fake, pms := newSlowFakePMS(t)
	ts, _ := newGenTestServer(t, pms.URL)

	first := fireLoad(ts, "100")
	fake.waitArrival(t, "100")
	second := fireLoad(ts, "200")
	fake.waitArrival(t, "200")
	fake.releaseAll()

	res1 := awaitLoad(t, first)
	if res1.out["cancelled"] == true {
		t.Fatalf("load for collection 100 was cancelled by an unrelated load for 200: %v", res1.out)
	}
	if got := loadItems(t, res1); len(got) != 2 {
		t.Fatalf("collection 100 items = %v, want 2", got)
	}
	res2 := awaitLoad(t, second)
	if res2.out["cancelled"] == true {
		t.Fatalf("load for collection 200 was cancelled: %v", res2.out)
	}
	if got := loadItems(t, res2); len(got) != 2 {
		t.Fatalf("collection 200 items = %v, want 2", got)
	}
}

// Two overlapping loads for the SAME collection: the newer one
// supersedes the stale one — the first answers cancelled+empty, the
// second completes with the members. (The detail view depends on
// exactly this; it must hold before and after the fix.)
func TestGetCollectionItemsSameKeySupersedes(t *testing.T) {
	fake, pms := newSlowFakePMS(t)
	ts, _ := newGenTestServer(t, pms.URL)

	first := fireLoad(ts, "100")
	fake.waitArrival(t, "100")
	second := fireLoad(ts, "100")

	// The stale load can only finish by being cancelled.
	res1 := awaitLoad(t, first)
	if res1.out["cancelled"] != true {
		t.Fatalf("stale same-key load should answer cancelled, got %v", res1.out)
	}
	if got := loadItems(t, res1); len(got) != 0 {
		t.Fatalf("cancelled load items = %v, want empty", got)
	}

	fake.waitArrival(t, "100") // the superseding load reaches the children endpoint
	fake.releaseAll()
	res2 := awaitLoad(t, second)
	if res2.out["cancelled"] == true {
		t.Fatalf("superseding load was cancelled: %v", res2.out)
	}
	if got := loadItems(t, res2); len(got) != 2 {
		t.Fatalf("superseding load items = %v, want 2", got)
	}
}
