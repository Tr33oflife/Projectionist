package api

// handlers_custompins.go — the free-text pin endpoints (v1.24, Tier 1).
//
// Pins are the app's own per-server record of user-typed badge text
// attached to titles (internal/store/custompins.go). These endpoints
// are its whole API: list the current server's pins, set one pin on a
// batch of items (the UI expands a collection pin into member refs
// before calling), remove one pin. Every mutating call answers with
// the FRESH list, so the manager renders exactly what is stored —
// never its own guess at the result.

import (
	"net/http"

	"plexcollectionmanager/internal/store"
)

// customPinsPayload builds the manager's list payload for one server:
// pins in display order (title, then text), each with its id so the
// UI can delete exactly one.
func (s *Server) customPinsPayload(machine string) (envelope, error) {
	pins, err := s.store.CustomPins(machine)
	if err != nil {
		return nil, err
	}
	list := make([]any, 0, len(pins))
	for _, p := range pins {
		list = append(list, map[string]any{
			"id": p.ID, "ratingKey": p.RatingKey, "title": p.Title,
			"text": p.Text, "color": p.Color, "corner": p.Corner,
			"variant": p.Variant, "source": p.Source,
		})
	}
	return envelope{"pins": list}, nil
}

func (s *Server) handleCustomPinsList(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	payload, err := s.customPinsPayload(server.Machine)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, payload)
}

func (s *Server) handleCustomPinsSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items   []store.CustomPinRef `json:"items"`
		Text    string               `json:"text"`
		Color   string               `json:"color"`
		Corner  string               `json:"corner"`
		Variant string               `json:"variant"`
		Source  string               `json:"source"`
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
	// The store validates the text as 1–40 characters and normalizes
	// color/corner; the UI never invents a pin, and neither does
	// this endpoint.
	if _, err := s.store.SetCustomPins(server.Machine, body.Items, body.Text, body.Color, body.Corner, body.Variant, body.Source); err != nil {
		failErr(w, err)
		return
	}
	payload, err := s.customPinsPayload(server.Machine)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, payload)
}

func (s *Server) handleCustomPinsRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
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
	if err := s.store.RemoveCustomPin(server.Machine, body.ID); err != nil {
		failErr(w, err)
		return
	}
	payload, err := s.customPinsPayload(server.Machine)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, payload)
}
