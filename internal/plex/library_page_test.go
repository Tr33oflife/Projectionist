package plex

// library_page_test.go — v1.17 Builder wizard grid plumbing.
// GetSectionItemsPage is the read-only paged listing behind the
// wizard's poster grid. The fake PMS below seeds 150 movies and
// honors the same container params real PMS does (start/size in the
// query, totalSize in the container), plus the `title=` search param
// and a raw filter segment (genre=Action matches every third movie).
// Also pinned here: a create carrying a content rating sends it
// through the SAME verified metadata write CreateCollection already
// performs (contentRating.value + .locked), never a new endpoint.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type pageFakePMS struct {
	putMetaQuery url.Values
}

func pageMovieTitle(i int) string {
	// Three "Zebra" titles spread across the library so a title search
	// provably narrows across pages, mirroring the DOM harness fake.
	switch i {
	case 1:
		return "Zebra Opening"
	case 76:
		return "Zebra Middle"
	case 150:
		return "Zebra Finale"
	}
	return fmt.Sprintf("Page Movie %03d", i)
}

func (f *pageFakePMS) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/library/sections/1/all" && r.Method == http.MethodGet:
		// Full seeded library: ratingKeys 1001..1150.
		type mv struct {
			rk, title string
			year      int
		}
		all := []mv{}
		for i := 1; i <= 150; i++ {
			if q.Get("genre") == "Action" && i%3 != 0 {
				continue
			}
			title := pageMovieTitle(i)
			if t := q.Get("title"); t != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(t)) {
				continue
			}
			all = append(all, mv{strconv.Itoa(1000 + i), title, 1950 + i%70})
		}
		start, _ := strconv.Atoi(q.Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(q.Get("X-Plex-Container-Size"))
		if size <= 0 {
			size = 50
		}
		if start > len(all) {
			start = len(all)
		}
		end := start + size
		if end > len(all) {
			end = len(all)
		}
		var b strings.Builder
		fmt.Fprintf(&b, `{"MediaContainer":{"size":%d,"totalSize":%d,"Metadata":[`, end-start, len(all))
		for j, m := range all[start:end] {
			if j > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"ratingKey":%s,"type":"movie","title":%q,"year":%d,"thumb":"/library/metadata/%s/thumb/1"}`, m.rk, m.title, m.year, m.rk)
		}
		b.WriteString(`]}}`)
		_, _ = w.Write([]byte(b.String()))
	case r.URL.Path == "/library/collections" && r.Method == http.MethodPost:
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":700,"type":"collection","title":"Rated One","smart":"1","childCount":2}]}}`))
	case r.URL.Path == "/library/metadata/700" && r.Method == http.MethodGet:
		// The write policy proves the target is a collection before
		// any metadata PUT — serve the record it checks.
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":700,"type":"collection","title":"Rated One","smart":"1","childCount":2,"librarySectionID":1}]}}`))
	case r.URL.Path == "/library/metadata/700" && r.Method == http.MethodPut:
		f.putMetaQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	default:
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	}
}

func pageTestClient(t *testing.T) (*Client, *Server, *pageFakePMS) {
	t.Helper()
	f := &pageFakePMS{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Machine: "fake-machine", Token: "t", Connections: []Connection{{URI: srv.URL, Local: true}}}
	return c, server, f
}

func TestGetSectionItemsPageAccumulates150(t *testing.T) {
	c, server, _ := pageTestClient(t)
	typ := 1
	seen := map[string]bool{}
	total := 0
	for start := 0; ; start += 60 {
		page, err := c.GetSectionItemsPage(context.Background(), server, "1", "", &typ, "", start, 60)
		if err != nil {
			t.Fatalf("GetSectionItemsPage(start=%d): %v", start, err)
		}
		if !page.TotalKnown || page.Total != 150 {
			t.Fatalf("page(start=%d) total = %d known=%v, want exact 150 from totalSize", start, page.Total, page.TotalKnown)
		}
		total += len(page.Items)
		for _, it := range page.Items {
			if seen[it.RatingKey] {
				t.Fatalf("duplicate item across pages: %s", it.RatingKey)
			}
			seen[it.RatingKey] = true
		}
		if !page.HasMore {
			if start+len(page.Items) != 150 {
				t.Fatalf("HasMore=false at start=%d with %d items, want the walk to end exactly at 150", start, len(page.Items))
			}
			break
		}
		if start > 300 {
			t.Fatal("paging did not terminate")
		}
	}
	if total != 150 || len(seen) != 150 {
		t.Fatalf("accumulated %d items (%d unique), want 150", total, len(seen))
	}
}

func TestGetSectionItemsPageSearchNarrowsByTitle(t *testing.T) {
	c, server, _ := pageTestClient(t)
	typ := 1
	page, err := c.GetSectionItemsPage(context.Background(), server, "1", "", &typ, "zebra", 0, 60)
	if err != nil {
		t.Fatalf("GetSectionItemsPage(search): %v", err)
	}
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("search page = total %d / %d items, want 3/3", page.Total, len(page.Items))
	}
	for _, it := range page.Items {
		if !strings.Contains(it.Title, "Zebra") {
			t.Errorf("search returned non-matching title %q", it.Title)
		}
	}
	if page.HasMore {
		t.Errorf("search page HasMore = true, want false at 3 of 3")
	}
}

func TestGetSectionItemsPageFilterTotal(t *testing.T) {
	c, server, _ := pageTestClient(t)
	typ := 1
	page, err := c.GetSectionItemsPage(context.Background(), server, "1", "genre=Action", &typ, "", 0, 24)
	if err != nil {
		t.Fatalf("GetSectionItemsPage(filter): %v", err)
	}
	// Every third of 150 movies = 50 matches; first page is 24.
	if page.Total != 50 || len(page.Items) != 24 || !page.HasMore {
		t.Fatalf("filter page = total %d / %d items / hasMore %v, want 50/24/true", page.Total, len(page.Items), page.HasMore)
	}
}

func TestCreateCollectionSendsContentRating(t *testing.T) {
	c, server, f := pageTestClient(t)
	out, err := c.CreateCollection(context.Background(), server, CreatePayload{
		SectionKey: "1", Type: 1, Title: "Rated One", Smart: true,
		FilterQuery: "genre=Action", ContentRating: "PG-13",
	})
	if err != nil {
		t.Fatalf("CreateCollection(with rating): %v", err)
	}
	if !strings.Contains(string(out), `"ratingKey":"700"`) {
		t.Fatalf("create result = %s, want ratingKey 700", out)
	}
	if got := f.putMetaQuery.Get("contentRating.value"); got != "PG-13" {
		t.Errorf("metadata write contentRating.value = %q, want PG-13 (query: %v)", got, f.putMetaQuery)
	}
	if got := f.putMetaQuery.Get("contentRating.locked"); got != "1" {
		t.Errorf("metadata write contentRating.locked = %q, want 1", got)
	}
}
