package api

// postergen_ai_test.go — the generator's "Generate with AI"
// background source, against fake providers for BOTH shapes:
// Cloudflare Workers AI (run endpoint, base64 in JSON) and
// OpenAI-shaped images endpoints (b64_json and URL variants).
// A provider 400 must surface its own message; with no key
// configured the answer is a plain pointer to Settings. With
// PCM_PROOF=1 the Cloudflare test also renders a hero poster with
// the generated background and writes generator-proof-ai.png to
// the project root.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configureAI points the app's AI settings at a fake provider and
// stores a key, through the same HTTP routes Settings uses.
func configureAI(t *testing.T, ts *httptest.Server, baseURL string) {
	t.Helper()
	_, out := post(t, ts.URL+"/api/meta/setKey", testToken, `{"which":"aiKey","value":"test-ai-key"}`)
	if out["ok"] != true {
		t.Fatalf("setKey: %v", out)
	}
	_, out = post(t, ts.URL+"/api/meta/setAiBaseUrl", testToken,
		fmt.Sprintf(`{"url":%q}`, baseURL))
	if out["ok"] != true {
		t.Fatalf("setAiBaseUrl: %v", out)
	}
}

func genAIImage(t *testing.T, ts *httptest.Server, body string) map[string]any {
	t.Helper()
	_, out := post(t, ts.URL+"/api/postergen/aiImage", testToken, body)
	return out
}

func TestPosterGenAICloudflare(t *testing.T) {
	genPNG := genSolidPNG(t, 480, 480, color.RGBA{200, 90, 30, 255})
	var sawAuth, sawPath string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "errors": []any{},
			"result": map[string]any{"image": base64.StdEncoding.EncodeToString(genPNG)},
		})
	}))
	defer provider.Close()

	_, pms := newGenFakePMS(t, 4)
	ts, _ := newGenTestServer(t, pms.URL)
	configureAI(t, ts, provider.URL+"/accounts/ACC123/ai/v1")

	out := genAIImage(t, ts, `{"prompt":"a foggy pumpkin patch at dusk"}`)
	if out["ok"] != true {
		t.Fatalf("aiImage: %v", out)
	}
	art := out["art"].(map[string]any)
	if art["assetId"] == "" || art["source"] != "AI-generated background" {
		t.Fatalf("art = %v", art)
	}
	if sawAuth != "Bearer test-ai-key" {
		t.Fatalf("provider saw Authorization %q", sawAuth)
	}
	if sawPath != "/accounts/ACC123/ai/run/@cf/black-forest-labs/flux-1-schnell" {
		t.Fatalf("provider path = %q — the run endpoint must derive from the chat base URL", sawPath)
	}

	// The generated image behaves like any other background asset:
	// it renders into a Hero preview.
	preview := genPreview(t, ts, fmt.Sprintf(
		`{"ratingKey":"900","template":"hero","title":"AI Patch","background":{"kind":"asset","assetId":%q},"focalX":0.5,"focalY":0.5}`,
		art["assetId"]))
	if preview["art"].(map[string]any)["assetId"] == "" {
		t.Fatalf("hero preview with AI background: %v", preview)
	}
	if os.Getenv("PCM_PROOF") == "1" {
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(
			preview["art"].(map[string]any)["preview"].(string), "data:image/png;base64,"))
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join("..", "..", "..", "generator-proof-ai.png")
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("proof render written: %s", out)
	}
}

func TestPosterGenAIOpenAIShapes(t *testing.T) {
	genPNG := genSolidPNG(t, 512, 768, color.RGBA{40, 80, 200, 255})
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(genPNG)
	}))
	defer imgSrv.Close()

	mode := "b64"
	var sawSize string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sawSize, _ = req["size"].(string)
		w.Header().Set("Content-Type", "application/json")
		if mode == "b64" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"b64_json": base64.StdEncoding.EncodeToString(genPNG)}}})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"url": imgSrv.URL + "/gen.png"}}})
		}
	}))
	defer provider.Close()

	_, pms := newGenFakePMS(t, 4)
	ts, _ := newGenTestServer(t, pms.URL)
	configureAI(t, ts, provider.URL+"/v1")

	out := genAIImage(t, ts, `{"prompt":"neon city rain","model":"image-model-x"}`)
	if out["ok"] != true || out["art"].(map[string]any)["assetId"] == "" {
		t.Fatalf("b64 variant: %v", out)
	}
	if sawSize != "1024x1536" {
		t.Fatalf("requested size = %q — portrait poster proportions expected", sawSize)
	}
	mode = "url"
	out = genAIImage(t, ts, `{"prompt":"neon city rain","model":"image-model-x"}`)
	if out["ok"] != true || out["art"].(map[string]any)["assetId"] == "" {
		t.Fatalf("url variant: %v", out)
	}
}

func TestPosterGenAIErrorsSurfaceRaw(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "prompt rejected: too spooky"}})
	}))
	defer provider.Close()

	_, pms := newGenFakePMS(t, 4)
	ts, _ := newGenTestServer(t, pms.URL)
	configureAI(t, ts, provider.URL+"/v1")

	out := genAIImage(t, ts, `{"prompt":"spooky","model":"image-model-x"}`)
	if out["ok"] != false || !strings.Contains(fmt.Sprint(out["error"]), "400") ||
		!strings.Contains(fmt.Sprint(out["error"]), "too spooky") {
		t.Fatalf("provider 400 must surface status + its message, got %v", out)
	}

	// Cloudflare failure shape: success=false with an errors list.
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []any{map[string]any{"message": "no such model"}},
		})
	}))
	defer cf.Close()
	configureAI(t, ts, cf.URL+"/accounts/ACC9/ai/v1")
	out = genAIImage(t, ts, `{"prompt":"anything"}`)
	if out["ok"] != false || !strings.Contains(fmt.Sprint(out["error"]), "no such model") {
		t.Fatalf("cloudflare failure must surface its message, got %v", out)
	}
}

func TestPosterGenAINoKeyGuidance(t *testing.T) {
	_, pms := newGenFakePMS(t, 4)
	ts, _ := newGenTestServer(t, pms.URL)
	out := genAIImage(t, ts, `{"prompt":"anything"}`)
	if out["ok"] != false || !strings.Contains(fmt.Sprint(out["error"])+fmt.Sprint(out["hint"]), "Settings") {
		t.Fatalf("no-key must answer with a plain pointer to Settings, got %v", out)
	}
	// An empty prompt is refused before any provider call.
	configureAI(t, ts, "http://127.0.0.1:1/v1")
	out = genAIImage(t, ts, `{"prompt":"  "}`)
	if out["ok"] != false || !strings.Contains(fmt.Sprint(out["error"]), "prompt") {
		t.Fatalf("empty prompt: %v", out)
	}
}
