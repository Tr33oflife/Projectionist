package api

// handlers_leaving.go — the Leaving list endpoints (v1.12).
//
// The Leaving list is the app's own per-server record of titles
// leaving the library and their dates (internal/store/leaving.go).
// These endpoints are its whole API: list the current server's
// entries, set one date on a batch of items, remove one entry. Every
// mutating call answers with the FRESH list, so the manager renders
// exactly what is stored — never its own guess at the result.
//
// Day counts are computed here (and in the facts layer) against the
// APP's local date — see store.DaysUntilLeaving for the clock rule.

import (
	"net/http"
	"time"

	"plexcollectionmanager/internal/store"
)

// leavingEntriesPayload builds the manager's list payload for one
// server: entries in display order (date, then title), each with its
// signed day count and expired flag computed against the app's local
// date. An entry whose stored date no longer parses (a hand-edited
// file) is still listed — with days null and dateError set — rather
// than silently dropped.
func (s *Server) leavingEntriesPayload(machine string) (envelope, error) {
	entries, err := s.store.LeavingEntries(machine)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	list := make([]any, 0, len(entries))
	for _, key := range store.SortedLeavingKeys(entries) {
		e := entries[key]
		row := map[string]any{
			"ratingKey": key,
			"title":     e.Title,
			"date":      e.Date,
		}
		if days, okDays := store.DaysUntilLeaving(e.Date, now); okDays {
			row["days"] = days
			row["expired"] = days < 0
		} else {
			row["days"] = nil
			row["expired"] = false
			row["dateError"] = "stored date is not a real YYYY-MM-DD date"
		}
		list = append(list, row)
	}
	return envelope{"entries": list}, nil
}

func (s *Server) handleLeavingList(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	payload, err := s.leavingEntriesPayload(server.Machine)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, payload)
}

func (s *Server) handleLeavingSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []store.LeavingItemRef `json:"items"`
		Date  string                 `json:"date"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	// The store validates the date as a real calendar date; the UI
	// never invents one, and neither does this endpoint.
	if err := s.store.SetLeaving(server.Machine, body.Items, body.Date); err != nil {
		failErr(w, err)
		return
	}
	payload, err := s.leavingEntriesPayload(server.Machine)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, payload)
}

func (s *Server) handleLeavingRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	if err := s.store.RemoveLeaving(server.Machine, body.RatingKey); err != nil {
		failErr(w, err)
		return
	}
	payload, err := s.leavingEntriesPayload(server.Machine)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, payload)
}
