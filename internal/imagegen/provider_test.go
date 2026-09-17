package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixturePNG(t *testing.T, transparent bool) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	if !transparent {
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				im.Set(x, y, color.NRGBA{R: 220, G: 40, B: 20, A: 255})
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func testConfig(t *testing.T, base string) Config {
	t.Helper()
	t.Setenv("TEST_IMAGE_TOKEN", "test-secret")
	return Config{DefaultModel: "demo", OutputDir: t.TempDir(), Concurrency: 1, QueueSize: 1, TaskTimeout: "5s", WaitTimeout: "1s", Retention: "0s", PreviewSize: 64, MaxImageBytes: 1 << 20, MaxImagePixels: 1_000_000, Models: map[string]Model{"demo": {Provider: "openai", BaseURL: base, TokenEnv: "TEST_IMAGE_TOKEN", Model: "test-model", Capabilities: []string{"generate", "edit", "mask"}, MaxImages: 2, MaxInputImages: 4, Parameters: map[string][]string{"size": {"1024x1024"}, "quality": {"high"}, "output_format": {"png"}}}}}
}
func respondImage(w http.ResponseWriter, b []byte) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(b)}}})
}
func TestOpenAIProtocols(t *testing.T) {
	pngData := fixturePNG(t, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/v1/images/generations":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["model"] != "test-model" || body["prompt"] != "hello" || body["n"] != float64(1) || body["quality"] != "high" {
				t.Errorf("bad generation body: %v", body)
			}
		case "/v1/images/edits":
			if err := r.ParseMultipartForm(4 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("model") != "test-model" || r.FormValue("prompt") != "hello" {
				t.Error("missing edit fields")
			}
			if len(r.MultipartForm.File["image[]"]) != 2 || len(r.MultipartForm.File["mask"]) != 1 {
				t.Error("missing multipart images/mask")
			}
			file, err := r.MultipartForm.File["image[]"][0].Open()
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			got, _ := io.ReadAll(file)
			if !bytes.Equal(got, pngData) {
				t.Error("image bytes corrupted")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		respondImage(w, pngData)
	}))
	defer server.Close()
	cfg := testConfig(t, server.URL+"/v1")
	store := NewImageStore(cfg)
	p := NewProvider(store)
	pic, err := store.Decode(pngData)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []bool{false, true} {
		r := Request{Prompt: "hello", N: 1, Quality: "high"}
		var inputs []Picture
		var mask *Picture
		if edit {
			inputs = []Picture{pic, pic}
			mask = &pic
		}
		out, err := p.Generate(context.Background(), cfg.Models["demo"], r, inputs, mask)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || !bytes.Equal(out[0].Data, pngData) {
			t.Fatal("wrong output")
		}
	}
}
func TestGeminiProtocol(t *testing.T) {
	pngData := fixturePNG(t, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:generateContent" || r.Header.Get("x-goog-api-key") != "test-secret" {
			t.Errorf("wrong Gemini endpoint/auth: %s", r.URL.Path)
		}
		var body struct {
			Contents   []struct{ Parts []map[string]any }
			Generation map[string]any `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Contents) != 1 || len(body.Contents[0].Parts) != 2 {
			t.Fatal("missing reference image")
		}
		ic := body.Generation["imageConfig"].(map[string]any)
		if ic["aspectRatio"] != "16:9" {
			t.Error("aspect ratio not mapped")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "generated"}, map[string]any{"inlineData": map[string]string{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(pngData)}}}}}}})
	}))
	defer server.Close()
	cfg := testConfig(t, server.URL+"/v1beta")
	m := cfg.Models["demo"]
	m.Provider = "gemini"
	m.Model = "gemini-test"
	p := NewProvider(NewImageStore(cfg))
	pic, _ := p.images.Decode(pngData)
	out, err := p.Generate(context.Background(), m, Request{Prompt: "edit", N: 1, AspectRatio: "16:9"}, []Picture{pic}, nil)
	if err != nil || len(out) != 1 {
		t.Fatalf("Gemini: %v", err)
	}
}
func TestInputBoundariesAndMask(t *testing.T) {
	cfg := testConfig(t, "http://localhost/v1")
	allowed := t.TempDir()
	outside := t.TempDir()
	cfg.AllowedInputDirs = []string{allowed}
	store := NewImageStore(cfg)
	data := fixturePNG(t, false)
	inside := filepath.Join(allowed, "image.png")
	secret := filepath.Join(outside, "image.png")
	if err := os.WriteFile(inside, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(allowed, "escape.png")); err != nil {
		t.Fatal(err)
	}
	for _, in := range []ImageInput{{Path: secret}, {Path: filepath.Join(allowed, "escape.png")}, {Path: inside, Data: "bad"}, {Data: base64.StdEncoding.EncodeToString([]byte("not an image"))}, {URL: "file:///etc/passwd"}, {URL: "http://127.0.0.1:1/image"}} {
		if _, err := store.Resolve(context.Background(), in); err == nil {
			t.Errorf("accepted forbidden input %+v", in)
		}
	}
	pic, err := store.Resolve(context.Background(), ImageInput{Path: inside})
	if err != nil {
		t.Fatal(err)
	}
	mask, err := store.Decode(fixturePNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if err = validateMask(pic, mask); err != nil {
		t.Fatal(err)
	}
	if err = validateMask(pic, pic); err == nil {
		t.Fatal("opaque mask accepted")
	}
	cfg.MaxImagePixels = 1
	if _, err = NewImageStore(cfg).Decode(data); err == nil {
		t.Fatal("oversized dimensions accepted")
	}
}
func TestValidationBeforeSubmission(t *testing.T) {
	cfg := testConfig(t, "http://localhost/v1")
	for _, r := range []Request{{Prompt: ""}, {Prompt: "x", N: 3}, {Prompt: "x", Quality: "unsupported"}, {Prompt: "x", AspectRatio: "1:1"}, {Prompt: "x", Extra: map[string]any{"base_url": "http://evil"}}, {Prompt: "x", Images: []ImageInput{{Data: "x"}}}} {
		if _, _, err := cfg.Normalize("generate", r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if _, _, err := cfg.Normalize("edit", Request{Prompt: "x"}); err == nil {
		t.Fatal("empty edit accepted")
	}
}

func TestGeminiResolutionAndRatio(t *testing.T) {
	cfg := testConfig(t, "http://localhost/v1beta")
	m := cfg.Models["demo"]
	m.Provider = "gemini"
	m.Capabilities = []string{"generate", "edit"}
	m.MaxImages = 1
	m.Parameters = map[string][]string{"size": {"2K"}, "aspect_ratio": {"16:9"}}
	cfg.Models["demo"] = m
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.Normalize("generate", Request{Prompt: "x", Size: "2K", AspectRatio: "16:9"}); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewResizesWithoutChangingOriginal(t *testing.T) {
	cfg := testConfig(t, "http://localhost/v1")
	store := NewImageStore(cfg)
	im := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), b.Bytes()...)
	pic, err := store.Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.Preview(pic)
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := image.DecodeConfig(bytes.NewReader(preview))
	if err != nil || info.Width != 64 || info.Height != 32 {
		t.Fatalf("wrong preview dimensions: %+v %v", info, err)
	}
	if !bytes.Equal(pic.Data, original) {
		t.Fatal("original was modified")
	}
}
func TestCompatibleURLResult(t *testing.T) {
	data := fixturePNG(t, false)
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("upstream credential leaked to asset host")
		}
		_, _ = w.Write(data)
	}))
	defer asset.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"url":"`+asset.URL+`/image"}]}`)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	store := NewImageStore(cfg)
	store.fetch = asset.Client() // Local fixture; production rejects private image hosts.
	out, err := NewProvider(store).Generate(context.Background(), cfg.Models["demo"], Request{Prompt: "x", N: 1}, nil, nil)
	if err != nil || len(out) != 1 {
		t.Fatalf("URL result: %v", err)
	}
}
func TestExampleConfig(t *testing.T) {
	cfg, err := LoadConfig("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.OutputDir) || len(cfg.Models) != 3 {
		t.Fatal("unexpected example config")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\nmisspelled_option: true\n")...)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(path); err == nil || !strings.Contains(err.Error(), "misspelled_option") {
		t.Fatal("unknown config field accepted")
	}
}
