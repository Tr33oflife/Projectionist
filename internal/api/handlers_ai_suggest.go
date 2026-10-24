package api

// handlers_ai_suggest.go — v1.21: POST /api/ai/suggestCollections.
// The Advisor's AI proposes collections for one library section from
// the most recent scan. All verification lives in
// advisor.SuggestCollections; this handler only assembles its
// inputs (scan, the section's existing collections with member keys
// for near-duplicate detection, the dismissal list) and shapes the
// envelope.
//
// The not-configured check runs FIRST, before any scan or Plex
// work: with no AI key the endpoint answers with the standard
// guidance and the AI client is never invoked — zero calls, by
// construction.

import (
	"net/http"
	"strings"

	"plexcollectionmanager/internal/advisor"
	"plexcollectionmanager/internal/plex"
)

func (s *Server) handleAISuggestCollections(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey string `json:"sectionKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if strings.TrimSpace(body.SectionKey) == "" {
		failMsg(w, "Choose a library section first.", "")
		return
	}
	ai := s.buildAIClient(s.getSecrets())
	if !ai.Configured() {
		failErr(w, &plex.Error{Message: aiNotConfiguredMsg, Hint: aiHintSuggestions})
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	scan, err := s.getFullScan()
	if err != nil {
		failErr(w, err)
		return
	}
	if scan == nil {
		failMsg(w, "No library scan yet", "Run a scan in the Advisor first — AI suggestions are built from the scan.")
		return
	}

	// Existing collections + their member keys, for duplicate
	// detection. The list itself failing fails the run (the dedupe
	// guarantee would silently not hold); ONE collection's members
	// failing degrades that collection to title-only detection.
	colls, err := s.plexClient.GetCollections(r.Context(), server, body.SectionKey)
	if err != nil {
		failErr(w, err)
		return
	}
	existing := make([]advisor.ExistingCollection, 0, len(colls))
	for _, c := range colls {
		ec := advisor.ExistingCollection{Title: c.Title}
		if items, ierr := s.plexClient.GetCollectionItems(r.Context(), server, c.RatingKey); ierr == nil {
			for _, it := range items {
				ec.Keys = append(ec.Keys, it.RatingKey)
			}
		}
		existing = append(existing, ec)
	}

	dismissedIDs, _ := s.advisorStore.GetDismissals()
	outcome, err := advisor.SuggestCollections(r.Context(), scan, body.SectionKey, ai, ai.Model, existing, dismissedIDs)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{
		"cards":      outcome.Cards,
		"calls":      outcome.Calls,
		"model":      ai.Model,
		"sawItems":   outcome.SawItems,
		"totalItems": outcome.TotalItems,
		"sampled":    outcome.Sampled,
		"suppressed": outcome.Suppressed,
	})
}
