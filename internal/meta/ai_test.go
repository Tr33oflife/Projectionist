package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- SanitizeRules --------------------------------------------------------

func TestSanitizeRules(t *testing.T) {
	t.Run("drops unknown fields and ops", func(t *testing.T) {
		in := []Rule{
			{Field: "genre", Op: "is", Value: "Drama"},
			{Field: "ratingKey", Op: "is", Value: "x"},   // unknown field
			{Field: "genre", Op: "contains", Value: "x"}, // unknown op
			{Field: "year", Op: "from", Value: "1990"},
		}
		got := SanitizeRules(in)
		if len(got) != 2 || got[0].Field != "genre" || got[1].Field != "year" {
			t.Errorf("got %+v, want only the genre and year rules", got)
		}
	})

	t.Run("caps at 8", func(t *testing.T) {
		in := make([]Rule, 0, 12)
		for i := 0; i < 12; i++ {
			in = append(in, Rule{Field: "genre", Op: "is", Value: fmt.Sprint(i)})
		}
		if got := SanitizeRules(in); len(got) != 8 {
			t.Errorf("len = %d, want cap of 8", len(got))
		}
	})

	t.Run("unwatched empty-value rule kept, other empty values dropped", func(t *testing.T) {
		in := []Rule{
			{Field: "unwatched", Op: "yes", Value: ""},
			{Field: "genre", Op: "is", Value: ""},
			{Field: "studio", Op: "is", Value: "  "},
		}
		got := SanitizeRules(in)
		if len(got) != 1 || got[0].Field != "unwatched" {
			t.Errorf("got %+v, want only the unwatched rule", got)
		}
	})

	t.Run("value capped at 120 chars", func(t *testing.T) {
		got := SanitizeRules([]Rule{{Field: "actor", Op: "is", Value: strings.Repeat("a", 200)}})
		if len(got) != 1 || len([]rune(got[0].Value)) != 120 {
			t.Errorf("got %+v, want one rule with a 120-char value", got)
		}
	})

	t.Run("non-array input equivalent: nil → empty", func(t *testing.T) {
		if got := SanitizeRules(nil); len(got) != 0 {
			t.Errorf("got %+v, want empty", got)
		}
	})
}

// ---- ExtractJSON ------------------------------------------------------------

func TestExtractJSON(t *testing.T) {
	t.Run("chatty text", func(t *testing.T) {
		raw, ok := ExtractJSON("Sure thing! Here you go: {\"rules\":[]} hope that helps")
		if !ok || string(raw) != `{"rules":[]}` {
			t.Errorf("got (%s, %v)", raw, ok)
		}
	})
	t.Run("no JSON", func(t *testing.T) {
		if _, ok := ExtractJSON("no json here"); ok {
			t.Error("want ok=false for no JSON")
		}
		if _, ok := ExtractJSON(""); ok {
			t.Error("want ok=false for empty input")
		}
	})
	t.Run("nested braces", func(t *testing.T) {
		in := `prefix {"a":{"b":{"c":1}},"d":[2,3]} suffix`
		raw, ok := ExtractJSON(in)
		if !ok {
			t.Fatal("want ok=true for nested braces")
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("extracted value does not parse: %v (%s)", err, raw)
		}
	})
	t.Run("invalid JSON object", func(t *testing.T) {
		if _, ok := ExtractJSON("{not json}"); ok {
			t.Error("want ok=false for unparseable braces")
		}
	})
}

// ---- AI chat: retries, fail-fast, proposals ---------------------------------

func aiServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *AIClient) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewAI("key123", srv.URL, "test-model")
	c.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return srv, c
}

func chatOK(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	_, _ = w.Write(body)
}

func TestAIRetry503ThenSuccess(t *testing.T) {
	var calls atomic.Int64
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		chatOK(w, `{"rules":[{"field":"genre","op":"is","value":"Thriller"}],"note":"ok"}`)
	})
	prop, err := c.ProposeRules(context.Background(), "90s thrillers")
	if err != nil {
		t.Fatalf("ProposeRules after retries: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (503, 503, 200)", calls.Load())
	}
	if prop == nil || len(prop.Rules) != 1 || prop.Rules[0].Value != "Thriller" || prop.Source != "ai" {
		t.Errorf("proposal = %+v, want one sanitized Thriller rule with source ai", prop)
	}
}

func TestAI401FailsFastWithBodyPreserved(t *testing.T) {
	var calls atomic.Int64
	longBody := `{"error":{"message":"` + strings.Repeat("model name hidden past one-sixty chars ", 10) + `unknown model: definitely-not-a-model"}}`
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(401)
		_, _ = w.Write([]byte(longBody))
	})
	_, err := c.TestConnection(context.Background())
	if err == nil {
		t.Fatal("want error on 401")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (401 fails fast, no retry)", calls.Load())
	}
	e, ok := err.(*Error)
	if !ok || e.Status != 401 {
		t.Fatalf("err = %#v, want *Error Status 401", err)
	}
	// The full >160-char body detail must survive (2000-char cap, not 160).
	if !strings.Contains(e.Message, "unknown model: definitely-not-a-model") {
		t.Errorf("error body was truncated before the model name: %q", e.Message)
	}
}

func TestAIUnconfiguredReturnsNilNil(t *testing.T) {
	c := NewAI("", "", "")
	if c.Configured() {
		t.Error("empty key must be unconfigured")
	}
	if c.BaseURL != DefaultBaseURL || c.Model != DefaultModel {
		t.Errorf("defaults = %q / %q, want %q / %q", c.BaseURL, c.Model, DefaultBaseURL, DefaultModel)
	}
	prop, err := c.ProposeRules(context.Background(), "anything")
	if prop != nil || err != nil {
		t.Errorf("unconfigured ProposeRules = (%v, %v), want (nil, nil)", prop, err)
	}
}

func TestAIUnparseableReturnsNilNil(t *testing.T) {
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		chatOK(w, "I cannot help with that, sorry.")
	})
	prop, err := c.ProposeRules(context.Background(), "comedies")
	if prop != nil || err != nil {
		t.Errorf("unparseable ProposeRules = (%v, %v), want (nil, nil)", prop, err)
	}
}

func TestAITestConnectionShape(t *testing.T) {
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "test-model" {
			t.Errorf("request model = %q, want the configured test-model", req.Model)
		}
		if r.Header.Get("Authorization") != "Bearer key123" {
			t.Errorf("Authorization = %q, want Bearer key123", r.Header.Get("Authorization"))
		}
		chatOK(w, "OK")
	})
	res, err := c.TestConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Model != "test-model" || res.BaseURL == "" || res.Reply != "OK" {
		t.Errorf("TestResult = %+v, want ok with model/baseUrl/reply", res)
	}
	raw, _ := json.Marshal(res)
	for _, k := range []string{`"ok"`, `"model"`, `"baseUrl"`, `"ms"`, `"reply"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("TestResult JSON %s missing key %s", raw, k)
		}
	}
}

func TestAIModelAndBaseURLSettings(t *testing.T) {
	c := NewAI("k", "https://ai.example.com/v1/", "sample-model-1")
	if c.BaseURL != "https://ai.example.com/v1" {
		t.Errorf("BaseURL = %q, want trailing slash stripped", c.BaseURL)
	}
	if c.Model != "sample-model-1" {
		t.Errorf("Model = %q, want the configured setting", c.Model)
	}
}
