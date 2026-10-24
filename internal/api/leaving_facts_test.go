package api

// leaving_facts_test.go — the Leaving list, end to end through the
// REAL API + facts handler against a fake PMS (v1.12):
//   - leaving/set through the API lands in the facts of that item on
//     that server (leavingSet / leavingDate / leavingDays), with the
//     day count judged against the app's local date.
//   - no entry → the three facts are unset (false / null / null);
//     the family never sees a guessed date.
//   - seasons and episodes never carry leaving facts — even when an
//     entry exists for their ratingKey (the facts layer gates by
//     type; the manager dates top-level titles).
//   - an invalid date is rejected by the endpoint and writes nothing.
//   - an expired entry stays listed (days negative, expired true)
//     and its facts keep carrying the truthful negative count — the
//     evaluator, not the facts layer, decides the banner is dark.
//   - leaving/remove returns the facts to unset.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fakeLeavingPMS(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(key, record string) {
		mux.HandleFunc("/library/metadata/"+key, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"MediaContainer":{"Metadata":[`+record+`]}}`)
		})
	}
	empty := func(path string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"MediaContainer":{"size":0,"Metadata":[]}}`)
		})
	}
	serve("700", `{"ratingKey":"700","type":"movie","title":"Leaving Movie","year":2024,"Guid":[]}`)
	serve("701", `{"ratingKey":"701","type":"show","title":"Leaving Show","year":2023,"Guid":[],"leafCount":4,"viewedLeafCount":0}`)
	serve("702", `{"ratingKey":"702","type":"season","title":"Season 1","parentRatingKey":"701","Guid":[]}`)
	serve("703", `{"ratingKey":"703","type":"episode","title":"Pilot","parentRatingKey":"702","grandparentRatingKey":"701","Guid":[]}`)
	empty("/library/metadata/701/allLeaves")
	empty("/library/metadata/702/children")
	return httptest.NewServer(mux)
}

// daysFromNow formats the app's local date n days out — the same
// civil arithmetic the store's day counter uses, so the expected
// counts below are exact.
func daysFromNow(n int) string {
	return time.Now().AddDate(0, 0, n).Format("2006-01-02")
}

func postLeaving(t *testing.T, app *httptest.Server, route, body string) map[string]any {
	t.Helper()
	_, out := post(t, app.URL+"/api/leaving/"+route, testToken, body)
	return out
}

func TestLeavingFactsEndToEnd(t *testing.T) {
	pms := fakeLeavingPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	// No entry: facts carry the unset triple.
	f := postFacts(t, app, "700")
	if f["leavingSet"] != false || f["leavingDate"] != nil || f["leavingDays"] != nil {
		t.Fatalf("no-entry facts = set %v / date %v / days %v, want false / nil / nil",
			f["leavingSet"], f["leavingDate"], f["leavingDays"])
	}

	// Set a date 10 days out through the API; the set response IS the
	// fresh list, with the day count already computed.
	date10 := daysFromNow(10)
	out := postLeaving(t, app, "set", fmt.Sprintf(
		`{"items":[{"ratingKey":"700","title":"Leaving Movie"}],"date":%q}`, date10))
	if out["ok"] != true {
		t.Fatalf("leaving/set failed: %v", out)
	}
	entries, _ := out["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries after set = %v, want exactly the one entry", out["entries"])
	}
	row, _ := entries[0].(map[string]any)
	if row["ratingKey"] != "700" || row["title"] != "Leaving Movie" || row["date"] != date10 {
		t.Errorf("entry row = %v, want 700 / Leaving Movie / %s", row, date10)
	}
	if row["days"] != float64(10) || row["expired"] != false {
		t.Errorf("entry days/expired = %v / %v, want 10 / false", row["days"], row["expired"])
	}

	// The facts now carry the entry.
	f = postFacts(t, app, "700")
	if f["leavingSet"] != true || f["leavingDate"] != date10 || f["leavingDays"] != float64(10) {
		t.Fatalf("facts after set = set %v / date %v / days %v, want true / %s / 10",
			f["leavingSet"], f["leavingDate"], f["leavingDays"], date10)
	}

	// A show carries it too (3 days out), independently of the movie.
	date3 := daysFromNow(3)
	out = postLeaving(t, app, "set", fmt.Sprintf(
		`{"items":[{"ratingKey":"701","title":"Leaving Show"}],"date":%q}`, date3))
	if out["ok"] != true {
		t.Fatalf("leaving/set (show) failed: %v", out)
	}
	f = postFacts(t, app, "701")
	if f["leavingSet"] != true || f["leavingDate"] != date3 || f["leavingDays"] != float64(3) {
		t.Fatalf("show facts = set %v / date %v / days %v, want true / %s / 3",
			f["leavingSet"], f["leavingDate"], f["leavingDays"], date3)
	}

	// Seasons and episodes never carry leaving facts — even with an
	// entry stored under their own ratingKey.
	for _, key := range []string{"702", "703"} {
		out = postLeaving(t, app, "set", fmt.Sprintf(
			`{"items":[{"ratingKey":%q,"title":"Bait"}],"date":%q}`, key, date3))
		if out["ok"] != true {
			t.Fatalf("leaving/set (bait %s) failed: %v", key, out)
		}
		f = postFacts(t, app, key)
		if f["leavingSet"] != false || f["leavingDate"] != nil {
			t.Errorf("facts(%s) carried leaving facts (set %v / date %v) — type gate failed",
				key, f["leavingSet"], f["leavingDate"])
		}
		postLeaving(t, app, "remove", fmt.Sprintf(`{"ratingKey":%q}`, key))
	}

	// An expired entry stays listed with a negative count and the
	// expired flag, and its facts tell the same truth.
	past := daysFromNow(-5)
	out = postLeaving(t, app, "set", fmt.Sprintf(
		`{"items":[{"ratingKey":"700","title":"Leaving Movie"}],"date":%q}`, past))
	if out["ok"] != true {
		t.Fatalf("leaving/set (past) failed: %v", out)
	}
	found := false
	for _, raw := range out["entries"].([]any) {
		row := raw.(map[string]any)
		if row["ratingKey"] == "700" {
			found = true
			if row["days"] != float64(-5) || row["expired"] != true {
				t.Errorf("expired row days/expired = %v / %v, want -5 / true", row["days"], row["expired"])
			}
		}
	}
	if !found {
		t.Error("expired entry missing from the list — expiry must not delete")
	}
	f = postFacts(t, app, "700")
	if f["leavingSet"] != true || f["leavingDays"] != float64(-5) {
		t.Errorf("expired facts = set %v / days %v, want true / -5 (facts stay truthful; the evaluator goes dark)",
			f["leavingSet"], f["leavingDays"])
	}

	// An invalid date is rejected and writes nothing.
	out = postLeaving(t, app, "set",
		`{"items":[{"ratingKey":"700","title":"Leaving Movie"}],"date":"2026-02-30"}`)
	if out["ok"] == true {
		t.Fatalf("leaving/set accepted 2026-02-30: %v", out)
	}
	f = postFacts(t, app, "700")
	if f["leavingDate"] != past {
		t.Errorf("facts date after rejected set = %v, want the untouched %s", f["leavingDate"], past)
	}

	// Remove returns the facts to unset and the list to empty for
	// that item.
	out = postLeaving(t, app, "remove", `{"ratingKey":"700"}`)
	if out["ok"] != true {
		t.Fatalf("leaving/remove failed: %v", out)
	}
	f = postFacts(t, app, "700")
	if f["leavingSet"] != false || f["leavingDate"] != nil || f["leavingDays"] != nil {
		t.Errorf("facts after remove = set %v / date %v / days %v, want the unset triple",
			f["leavingSet"], f["leavingDate"], f["leavingDays"])
	}
	out = postLeaving(t, app, "list", `{}`)
	if list, _ := out["entries"].([]any); len(list) != 1 {
		t.Errorf("list after removes = %v entries, want 1 (the show's entry remains)", out["entries"])
	}
}
