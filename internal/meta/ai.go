package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AI hook — port of src/metadata/ai.js. BYOK, OFF by default, proposes
// and never decides. Read the contract before touching this file:
//
//   - OPTIONAL chrome on a fully working deterministic Advisor. No key
//     configured → every method returns (nil, nil) and nothing changes.
//   - The AI NEVER supplies facts. No ratings, runtimes, languages, dub
//     verdicts, or member lists. It produces PROPOSALS (rule sets, merge
//     suggestions, ribbon wording) that the deterministic pipeline
//     validates. SanitizeRules is that validation for rules: anything
//     outside the Builder's allowed field/op sets is DROPPED.
//   - One OpenAI-compatible chat-completions endpoint: POST
//     {baseUrl}/chat/completions, Authorization: Bearer <key>, JSON body
//     {model, messages, max_tokens, temperature}.
//   - The MODEL IS A SETTING, never a constant baked into request code:
//     Google retires model names and 'gpt-4o-mini' does not exist on
//     Google's OpenAI-compatible endpoint — a hardcoded model 404s there
//     (that bug shipped once; the v0.3.1 fix made the model a setting).
//   - Unparseable replies → (nil, nil), never an error. The AI is never
//     on the critical path.
//   - Error bodies are surfaced up to 2000 chars (the Electron app once
//     truncated a body at 160 chars and hid a model name — do not repeat
//     that; see chat()).
//
// VERIFICATION STATUS: UNVERIFIED live (same as the JS).

// DefaultBaseURL is the JS DEFAULT_BASE_URL.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultModel is the JS DEFAULT_MODEL — a fallback only, used when the
// caller passes an empty model to NewAI. It is NOT hardcoded into
// requests; AIClient.Model is.
const DefaultModel = "gpt-4o-mini"

// AllowedRuleFields is ALLOWED_RULE_FIELDS from the JS, exactly: the
// rule fields the Builder understands. The AI may only propose these.
var AllowedRuleFields = []string{
	"genre", "year", "decade", "contentRating", "studio", "actor", "director",
	"audienceRating", "unwatched",
}

// AllowedOps is ALLOWED_OPS from the JS, exactly.
var AllowedOps = []string{"is", "from", "to", "min", "yes"}

func inSet(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Rule is one Builder rule: {field, op, value}.
type Rule struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// SanitizeRules drops anything the Builder can't execute (JS
// sanitizeRules()): unknown fields/ops are dropped, values are capped
// at 120 chars, rules with an empty value are dropped EXCEPT
// 'unwatched' (whose rule is meaningful with an empty value), and the
// result is capped at 8 rules.
func SanitizeRules(in []Rule) []Rule {
	out := make([]Rule, 0, len(in))
	for _, r := range in {
		if !inSet(AllowedRuleFields, r.Field) || !inSet(AllowedOps, r.Op) {
			continue
		}
		r.Value = truncateRunes(r.Value, 120)
		if r.Field != "unwatched" && strings.TrimSpace(r.Value) == "" {
			continue
		}
		out = append(out, r)
		if len(out) == 8 { // sanity cap — more rules than this is noise
			break
		}
	}
	return out
}

// ExtractJSON pulls the first {...} JSON object out of a chatty model
// reply (JS extractJson()): from the first '{' to the last '}', parsed.
// ok is false when there is no parseable object. It returns the raw
// object bytes (the JS returned the parsed value; Go callers unmarshal
// the raw message into the proposal shape, which is stricter).
func ExtractJSON(s string) (json.RawMessage, bool) {
	if s == "" {
		return nil, false
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, false
	}
	raw := s[start : end+1]
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

// RulesProposal is the JS proposeRules() return:
// {rules, note, source: 'ai'}.
type RulesProposal struct {
	Rules  []Rule `json:"rules"`
	Note   string `json:"note"`
	Source string `json:"source"`
}

// Merge is one JS merges entry: {names, suggestedTitle}.
type Merge struct {
	Names          []string `json:"names"`
	SuggestedTitle string   `json:"suggestedTitle"`
}

// MergeProposal is the JS proposeFranchiseMerge() return:
// {merges, note, source: 'ai'}.
type MergeProposal struct {
	Merges []Merge `json:"merges"`
	Note   string  `json:"note"`
	Source string  `json:"source"`
}

// RibbonProposal is the JS suggestRibbonText() return:
// {suggestions, source: 'ai'}.
type RibbonProposal struct {
	Suggestions []string `json:"suggestions"`
	Source      string   `json:"source"`
}

// TestResult is the JS testConnection() return:
// {ok, model, baseUrl, ms, reply}.
type TestResult struct {
	OK      bool   `json:"ok"`
	Model   string `json:"model"`
	BaseURL string `json:"baseUrl"`
	MS      int64  `json:"ms"`
	Reply   string `json:"reply"`
}

// defaultRetryDelays is RETRY_DELAYS_MS from the JS: free-tier providers
// (AI Studio) answer 429/503 under load; those are transient, so chat
// retries them with 2s / 6s / 14s backoff. Anything else fails fast.
var defaultRetryDelays = []time.Duration{2 * time.Second, 6 * time.Second, 14 * time.Second}

// retryableStatuses: 429 and 503 only (JS RETRYABLE).
func retryableStatus(status int) bool { return status == 429 || status == 503 }

// AIClient is the Go port of the JS AiClient. It constructs happily with
// an empty key — "off by default" is the contract; do NOT throw here.
type AIClient struct {
	// RetryDelays is the 429/503 backoff schedule (defaults to
	// 2s/6s/14s). It is an exported field so tests can shrink it; set
	// it to an empty slice to disable retries.
	RetryDelays []time.Duration

	// BaseURL and Model are the constructor settings. Model MUST stay
	// configurable (see the file header).
	BaseURL string
	Model   string

	apiKey     string
	httpClient *http.Client
}

// NewAI builds an AI client. Empty baseURL → DefaultBaseURL; empty
// model → DefaultModel. One trailing '/' is stripped from the base URL
// (JS: .replace(/\/$/, ”)); the model is trimmed.
func NewAI(apiKey, baseURL, model string) *AIClient {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel
	}
	return &AIClient{
		RetryDelays: append([]time.Duration(nil), defaultRetryDelays...),
		BaseURL:     baseURL,
		Model:       model,
		apiKey:      apiKey,
		httpClient:  &http.Client{},
	}
}

// Configured reports whether a key is set (JS `configured` getter).
func (c *AIClient) Configured() bool { return c.apiKey != "" }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

// chat is the JS _chat(): one chat-completions round trip, retrying
// HTTP 429/503 with RetryDelays backoff, failing fast on everything
// else with the status and up to 2000 chars of the error body.
// Unconfigured → ("", nil), the JS null.
func (c *AIClient) chat(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if !c.Configured() {
		return "", nil
	}
	body, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:   maxTokens,
		Temperature: 0.3,
	})
	if err != nil {
		return "", err
	}
	delays := c.RetryDelays
	if delays == nil {
		delays = defaultRetryDelays
	}
	attempt := 0
	for {
		reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		res, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			return "", &Error{
				Message: fmt.Sprintf("Could not reach the AI endpoint (%s)", err.Error()),
				Hint:    "Check the base URL in Settings → API keys.",
			}
		}
		resBody, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		cancel()
		if readErr != nil {
			return "", readErr
		}
		if res.StatusCode >= 200 && res.StatusCode <= 299 {
			var data struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			// JS tolerated an unparseable success body as {} → ''.
			_ = json.Unmarshal(resBody, &data)
			if len(data.Choices) == 0 {
				return "", nil
			}
			return data.Choices[0].Message.Content, nil
		}
		if retryableStatus(res.StatusCode) && attempt < len(delays) {
			if err := sleepCtx(ctx, delays[attempt]); err != nil {
				return "", err
			}
			attempt++
			continue
		}
		msg := fmt.Sprintf("AI endpoint returned HTTP %d", res.StatusCode)
		if len(resBody) > 0 {
			// 2000 chars, NOT 160: the Electron app once truncated an
			// error body at 160 chars and hid the failing model name.
			msg += ": " + bodySnippet(resBody, 2000)
		}
		hint := ""
		switch {
		case res.StatusCode == 401:
			hint = "The AI API key was rejected — check it in Settings."
		case retryableStatus(res.StatusCode):
			hint = "The free tier is at capacity after retries — try again in a few minutes."
		}
		return "", &Error{Message: msg, Status: res.StatusCode, Hint: hint}
	}
}

// TestConnection sends a tiny prompt and returns the raw result
// {ok, model, baseUrl, ms, reply}. Any failure returns the exact error
// (status + body snippet) — this is the instrument for diagnosing AI
// misconfig (wrong base URL, unknown model, bad key), not a silent
// boolean. Unconfigured is an error here, as in the JS.
func (c *AIClient) TestConnection(ctx context.Context) (*TestResult, error) {
	if !c.Configured() {
		return nil, &Error{
			Message: "No AI API key is set",
			Hint:    "Paste a key in Settings → API keys first.",
		}
	}
	started := time.Now()
	reply, err := c.chat(ctx, "You are a connectivity probe. Reply with exactly: OK", "ping", 10)
	if err != nil {
		return nil, err
	}
	return &TestResult{
		OK:      true,
		Model:   c.Model,
		BaseURL: c.BaseURL,
		MS:      time.Since(started).Milliseconds(),
		Reply:   truncateRunes(reply, 200),
	}, nil
}

// ProposeRules is plug point 1 (JS proposeRules()): NL → rules. The
// caller shows these as editable rules in the Builder; Plex validates
// them. Returns (nil, nil) when unconfigured OR unparseable OR the
// sanitized rule set is empty — the caller falls back, never errors.
func (c *AIClient) ProposeRules(ctx context.Context, text string) (*RulesProposal, error) {
	reply, err := c.chat(ctx,
		"You turn movie/TV collection requests into Plex smart-rule proposals. "+
			"Reply with ONLY a JSON object: {\"rules\":[{\"field\":\"<one of genre,year,decade,contentRating,studio,actor,director,audienceRating,unwatched>\",\"op\":\"<one of is,from,to,min,yes>\",\"value\":\"<string>\"}],\"note\":\"<one short sentence>\"}. "+
			"Rules combine with AND. Year ops: is/from/to. audienceRating uses op min. unwatched uses op yes with empty value. Max 5 rules. Never invent facts.",
		truncateRunes(text, 500), 600)
	if err != nil {
		return nil, err
	}
	if reply == "" {
		return nil, nil
	}
	raw, ok := ExtractJSON(reply)
	if !ok {
		return nil, nil
	}
	var parsed struct {
		Rules []Rule `json:"rules"`
		Note  string `json:"note"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil
	}
	rules := SanitizeRules(parsed.Rules)
	if len(rules) == 0 {
		return nil, nil
	}
	return &RulesProposal{
		Rules:  rules,
		Note:   truncateRunes(parsed.Note, 200),
		Source: "ai",
	}, nil
}

// ProposeFranchiseMerge is plug point 2 (JS proposeFranchiseMerge()):
// given TMDb collection names that look like one saga, propose a merge.
// the owner confirms before anything merges — this only suggests. Only
// merges whose names are ALL from the input survive (no invented
// names). Returns (nil, nil) for empty input, unconfigured, or
// unparseable replies.
func (c *AIClient) ProposeFranchiseMerge(ctx context.Context, names []string) (*MergeProposal, error) {
	if len(names) > 40 {
		names = names[:40]
	}
	if len(names) == 0 {
		return nil, nil
	}
	reply, err := c.chat(ctx,
		"You are given TMDb collection names from a Plex library. Reply with ONLY a JSON object: "+
			"{\"merges\":[{\"names\":[\"<exact input names>\"],\"suggestedTitle\":\"<saga title>\"}],\"note\":\"<one short sentence>\"}. "+
			"Only merge names that are obviously the same saga (e.g. different volumes/arcs of one franchise). "+
			"Copy input names EXACTLY. If nothing should merge, reply {\"merges\":[],\"note\":\"nothing to merge\"}.",
		strings.Join(names, "\n"), 600)
	if err != nil {
		return nil, err
	}
	if reply == "" {
		return nil, nil
	}
	raw, ok := ExtractJSON(reply)
	if !ok {
		return nil, nil
	}
	var parsed struct {
		Merges *[]Merge `json:"merges"`
		Note   string   `json:"note"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Merges == nil {
		return nil, nil // JS: !Array.isArray(parsed.merges) → null
	}
	input := make(map[string]bool, len(names))
	for _, n := range names {
		input[n] = true
	}
	merges := make([]Merge, 0, len(*parsed.Merges))
	for _, m := range *parsed.Merges {
		if len(m.Names) < 2 {
			continue
		}
		allIn := true
		for _, n := range m.Names {
			if !input[n] {
				allIn = false
				break
			}
		}
		if !allIn {
			continue
		}
		m.SuggestedTitle = truncateRunes(m.SuggestedTitle, 120)
		merges = append(merges, m)
		if len(merges) == 10 {
			break
		}
	}
	return &MergeProposal{
		Merges: merges,
		Note:   truncateRunes(parsed.Note, 200),
		Source: "ai",
	}, nil
}

// SuggestRibbonText is plug point 3 (JS suggestRibbonText()): short
// poster ribbon wordings for the overlay studio. Pure text ideas, no
// facts. Returns (nil, nil) when unconfigured, unparseable, or no
// usable suggestion came back.
func (c *AIClient) SuggestRibbonText(ctx context.Context, contextText string) (*RibbonProposal, error) {
	reply, err := c.chat(ctx,
		"Suggest short poster ribbon wordings (2-4 words each, ALL CAPS) for a media collection. "+
			"Reply with ONLY a JSON object: {\"suggestions\":[\"...\",\"...\",\"...\"]}. Max 6 suggestions.",
		truncateRunes(contextText, 300), 600)
	if err != nil {
		return nil, err
	}
	if reply == "" {
		return nil, nil
	}
	raw, ok := ExtractJSON(reply)
	if !ok {
		return nil, nil
	}
	var parsed struct {
		Suggestions *[]string `json:"suggestions"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Suggestions == nil {
		return nil, nil // JS: !Array.isArray(parsed.suggestions) → null
	}
	suggestions := make([]string, 0, len(*parsed.Suggestions))
	for _, s := range *parsed.Suggestions {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		suggestions = append(suggestions, truncateRunes(s, 40))
		if len(suggestions) == 6 {
			break
		}
	}
	if len(suggestions) == 0 {
		return nil, nil
	}
	return &RibbonProposal{Suggestions: suggestions, Source: "ai"}, nil
}

// ExtractJSONArray pulls the first [...] JSON array out of a chatty
// model reply — the array sibling of ExtractJSON: from the first '['
// to the last ']', verified parseable as a JSON array.
func ExtractJSONArray(s string) (json.RawMessage, bool) {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end <= start {
		return nil, false
	}
	cand := s[start : end+1]
	var probe []json.RawMessage
	if json.Unmarshal([]byte(cand), &probe) != nil {
		return nil, false
	}
	return json.RawMessage(cand), true
}

// CollectionProposal is one AI-proposed collection (v1.21): a title,
// a one-line summary for the collection, the rationale shown as the
// suggestion card's reason, and the member ratingKeys. The model is
// instructed to pick keys ONLY from the batch it was given, but it
// can still invent keys — the CALLER must verify every key against
// the real library before anything is shown (advisor.SuggestCollections
// does exactly that; nothing here trusts the reply).
type CollectionProposal struct {
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	Rationale        string   `json:"rationale"`
	MemberRatingKeys []string `json:"memberRatingKeys"`
}

// ProposeCollections is plug point 4 (v1.21): ONE batch call asking
// the model to propose collections from a compact listing of real
// library items (built by the caller). The failure contract here is
// DELIBERATELY stricter than the proposal methods above: this
// feature runs on an explicit click in the Advisor and the owner
// asked for a plain error over silent nothing, so an unparseable or
// empty reply is an ERROR, never (nil, nil). A reply that parses to
// an empty array is a valid "nothing worth suggesting in this batch"
// and returns an empty slice with a nil error.
func (c *AIClient) ProposeCollections(ctx context.Context, user string) ([]CollectionProposal, error) {
	reply, err := c.chat(ctx,
		"You are a collection curator for a personal media library. You are given a batch of titles "+
			"from ONE library section, one per line, in the form: ratingKey|title|year|genres|content rating|score. "+
			"Propose 2 to 4 collections that group titles from THIS batch in ways a person would enjoy browsing: "+
			"shared moods, eras, themes, directors' styles, comfort rewatches, seasonal picks — avoid merely restating a single genre. "+
			"Rules: choose member ratingKeys ONLY from the keys listed in this batch, never invent a key; "+
			"every collection needs at least 5 members and at most 40; do not propose a collection whose title "+
			"appears in the avoid lists included in the message. "+
			"Reply with ONLY a JSON array (no markdown fences, no commentary) of objects: "+
			"{\"title\":\"...\",\"summary\":\"one short line describing the collection\",\"rationale\":\"one or two sentences on why these titles belong together\",\"memberRatingKeys\":[\"...\"]}.",
		user, 2200)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(reply) == "" {
		return nil, &Error{Message: "The AI returned an empty reply.", Hint: "Try again — if it persists, check the model name in Settings."}
	}
	raw, ok := ExtractJSONArray(reply)
	if !ok {
		return nil, &Error{Message: "The AI's reply could not be parsed as collection suggestions.", Hint: "Try again — model replies vary. If it persists, try a different model in Settings."}
	}
	// memberRatingKeys is parsed loosely on purpose: models commonly
	// echo keys back as bare numbers even when the batch printed them
	// as strings. Structure stays strict — a reply that is not an
	// array of proposal objects is still a hard error.
	var parsed []struct {
		Title     string            `json:"title"`
		Summary   string            `json:"summary"`
		Rationale string            `json:"rationale"`
		Keys      []json.RawMessage `json:"memberRatingKeys"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &Error{Message: "The AI's reply could not be parsed as collection suggestions.", Hint: "Try again — model replies vary. If it persists, try a different model in Settings."}
	}
	out := make([]CollectionProposal, 0, len(parsed))
	for _, p := range parsed {
		prop := CollectionProposal{
			Title:     strings.TrimSpace(p.Title),
			Summary:   strings.TrimSpace(p.Summary),
			Rationale: strings.TrimSpace(p.Rationale),
		}
		for _, k := range p.Keys {
			var s string
			if json.Unmarshal(k, &s) == nil {
				s = strings.TrimSpace(s)
			} else {
				var n json.Number
				if json.Unmarshal(k, &n) == nil {
					s = n.String()
				}
			}
			if s != "" {
				prop.MemberRatingKeys = append(prop.MemberRatingKeys, s)
			}
		}
		out = append(out, prop)
	}
	return out, nil
}
