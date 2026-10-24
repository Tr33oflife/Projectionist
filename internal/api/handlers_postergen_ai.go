package api

// handlers_postergen_ai.go — "Generate with AI" for the poster
// generator (v1.20): one AI-generated background image for the Hero /
// Title card templates, using the AI provider already configured in
// Settings (base URL + API key). The image model is chosen in the
// generator panel, NOT in Settings — the chat model there is a text
// model and would be wrong for images.
//
// Two provider shapes:
//   - Cloudflare Workers AI: when the configured base URL is a
//     Cloudflare account endpoint (…/accounts/<id>/ai/v1), the image
//     runs on …/accounts/<id>/ai/run/<image-model> with the same
//     token; the JSON reply carries the image as base64. Workers AI
//     image models answer square images — the generator's cover-fit +
//     focal step crops to poster shape.
//   - OpenAI-shaped endpoints: POST <base>/images/generations with
//     {model, prompt, size: "1024x1536"} (portrait, where the
//     provider accepts a size); the reply's data[0] carries either
//     b64_json or a URL (fetched through the validated image fetch).
//
// The result is registered as a session art asset — exactly like an
// uploaded image: session-only, and it reaches Plex only if the
// owner applies it through the backup-gated setPoster path.
// Provider errors surface raw (status + body snippet), the same
// contract as the AI Test connection button.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
	"plexcollectionmanager/internal/postergen"
)

// defaultAIImageModel prefills the generator panel's model field.
const defaultAIImageModel = "@cf/black-forest-labs/flux-1-schnell"

func isCloudflareAIBase(base string) bool {
	return strings.Contains(base, "/accounts/") && strings.HasSuffix(base, "/ai/v1")
}

// cloudflareRunURL derives the run endpoint from the chat base URL:
// …/accounts/<id>/ai/v1 → …/accounts/<id>/ai/run/<model>.
func cloudflareRunURL(base, model string) string {
	return strings.TrimSuffix(base, "/v1") + "/run/" + model
}

func (s *Server) handlePosterGenAIImage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		failMsg(w, "Write a prompt first — a sentence describing the background you want.", "")
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = defaultAIImageModel
	}
	sec := s.getSecrets()
	if sec.aiKey == "" {
		failMsg(w, "No AI provider is configured.", "Add an AI base URL and API key in Settings → API keys & AI first, then come back here.")
		return
	}
	base := s.store.SettingString("aiBaseUrl")
	if base == "" {
		base = meta.DefaultBaseURL
	}
	base = strings.TrimSuffix(base, "/")

	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	var (
		data []byte
		err  error
	)
	if isCloudflareAIBase(base) {
		data, err = generateImageCloudflare(ctx, base, sec.aiKey, model, prompt)
	} else {
		data, err = generateImageOpenAIShaped(ctx, base, sec.aiKey, model, prompt)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	art, err := s.art.ImportGenerated(data, "AI-generated background", "AI-generated background")
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"art": art})
}

// aiProviderError is the raw-surface error: HTTP status + a body
// snippet, exactly what the owner needs to fix the provider config.
func aiProviderError(status int, body []byte) error {
	snip := strings.TrimSpace(string(body))
	if len(snip) > 400 {
		snip = snip[:400] + "…"
	}
	if snip == "" {
		snip = "(empty reply)"
	}
	return &plex.Error{Message: fmt.Sprintf("The image provider answered %d: %s", status, snip)}
}

func postAIImage(ctx context.Context, url, key string, payload any) (int, http.Header, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 150 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, &plex.Error{
			Message: fmt.Sprintf("Could not reach the image provider (%s)", err.Error()),
			Hint:    "Check the AI base URL in Settings → API keys & AI.",
		}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return 0, nil, nil, err
	}
	return res.StatusCode, res.Header, data, nil
}

// generateImageCloudflare runs a Workers AI image model. The JSON
// reply is {result:{image:"<base64>"}, success, errors:[…]}; some
// models answer with the raw image bytes instead — both are handled.
func generateImageCloudflare(ctx context.Context, base, key, model, prompt string) ([]byte, error) {
	status, header, raw, err := postAIImage(ctx, cloudflareRunURL(base, model), key,
		map[string]any{"prompt": prompt})
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(header.Get("Content-Type"), "image/") {
		if status != http.StatusOK {
			return nil, aiProviderError(status, raw)
		}
		return raw, nil
	}
	var resp struct {
		Result struct {
			Image string `json:"image"`
		} `json:"result"`
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, aiProviderError(status, raw)
	}
	if status != http.StatusOK || !resp.Success {
		if len(resp.Errors) > 0 && resp.Errors[0].Message != "" {
			return nil, &plex.Error{Message: fmt.Sprintf("The image provider answered %d: %s", status, resp.Errors[0].Message)}
		}
		return nil, aiProviderError(status, raw)
	}
	if resp.Result.Image == "" {
		return nil, &plex.Error{Message: "The image provider answered without an image. Try another prompt or model."}
	}
	data, err := base64.StdEncoding.DecodeString(resp.Result.Image)
	if err != nil {
		return nil, &plex.Error{Message: "The image provider's reply was not valid base64 image data."}
	}
	return data, nil
}

// generateImageOpenAIShaped calls POST <base>/images/generations.
// Portrait size is requested where the provider takes one; a
// returned URL is fetched through the validated image discipline.
func generateImageOpenAIShaped(ctx context.Context, base, key, model, prompt string) ([]byte, error) {
	status, _, raw, err := postAIImage(ctx, base+"/images/generations", key,
		map[string]any{"model": model, "prompt": prompt, "size": "1024x1536", "n": 1})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		// Prefer the structured error message when there is one.
		var er struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &er) == nil && er.Error.Message != "" {
			return nil, &plex.Error{Message: fmt.Sprintf("The image provider answered %d: %s", status, er.Error.Message)}
		}
		return nil, aiProviderError(status, raw)
	}
	var resp struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.Data) == 0 {
		return nil, &plex.Error{Message: "The image provider's reply had no image data. Try another prompt or model."}
	}
	first := resp.Data[0]
	if first.B64JSON != "" {
		data, err := base64.StdEncoding.DecodeString(first.B64JSON)
		if err != nil {
			return nil, &plex.Error{Message: "The image provider's reply was not valid base64 image data."}
		}
		return data, nil
	}
	if first.URL != "" {
		img, err := postergen.FetchImageURL(ctx, first.URL)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return nil, &plex.Error{Message: "The image provider's reply had no image data. Try another prompt or model."}
}
