package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

func TestInlineTokenFromYAML(t *testing.T) {
	for _, provider := range []string{"openai", "openai-compatible", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			const secret = "inline-test-secret"
			data := fixturePNG(t, false)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header, want := "Authorization", "Bearer "+secret
				if provider == "gemini" {
					header, want = "x-goog-api-key", secret
				}
				if r.Header.Get(header) != want {
					t.Error("configured inline token was not used")
				}
				if provider == "gemini" {
					_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(data), "mimeType": "image/png"}}}}}}})
					return
				}
				respondImage(w, data)
			}))
			t.Cleanup(upstream.Close)
			cfg := testConfig(t, upstream.URL)
			t.Setenv("TEST_IMAGE_TOKEN", "")
			model := cfg.Models["demo"]
			model.Provider = provider
			model.Token = secret
			model.TokenEnv = ""
			if provider == "gemini" {
				model.Capabilities = []string{"generate", "edit"}
				model.MaxImages = 1
				model.Parameters = nil
			}
			cfg.Models["demo"] = model
			path := filepath.Join(t.TempDir(), "config.yaml")
			encoded, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			e := startEngine(t, loaded)
			client := connectTestMCP(t, e)
			catalog, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_image_models", Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			var models modelList
			b, _ := json.Marshal(catalog.StructuredContent)
			if err = json.Unmarshal(b, &models); err != nil {
				t.Fatal(err)
			}
			if len(models.Models) != 1 || !models.Models[0].Configured {
				t.Fatal("inline model not reported as configured")
			}
			task, result := callTaskTool(t, client, "generate_image", Request{Prompt: "test direct token", RequestID: "direct-key"})
			if result.IsError || task.Status != "succeeded" {
				t.Fatalf("inline credential generation failed: status=%s", task.Status)
			}
			for _, v := range []any{catalog, result, loaded} {
				b, _ := json.Marshal(v)
				if bytes.Contains(b, []byte(secret)) {
					t.Fatal("inline token leaked through JSON serialization")
				}
			}
		})
	}
}

func TestInlineTokenPrecedesEnvironment(t *testing.T) {
	t.Setenv("TOKEN_PRECEDENCE_TEST", "environment-value")
	m := Model{Token: "inline-value", TokenEnv: "TOKEN_PRECEDENCE_TEST"}
	if m.credential() != "inline-value" {
		t.Fatal("inline token should take precedence")
	}
	m.Token = ""
	if m.credential() != "environment-value" {
		t.Fatal("environment fallback no longer works")
	}
}

func TestInlineTokenQueueRecoveryAndRotation(t *testing.T) {
	const oldSecret = "old-inline-secret"
	const newSecret = "new-inline-secret"
	data := fixturePNG(t, false)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+newSecret {
			t.Error("recovered task did not read current inline credential")
		}
		respondImage(w, data)
	}))
	t.Cleanup(upstream.Close)
	cfg := testConfig(t, upstream.URL)
	t.Setenv("TEST_IMAGE_TOKEN", "")
	m := cfg.Models["demo"]
	m.Token = oldSecret
	m.TokenEnv = ""
	cfg.Models["demo"] = m
	e := startEngine(t, cfg)
	e.stop()
	e.wg.Wait()
	r := Request{Prompt: "recover inline credentials", RequestID: "inline-restart"}
	task, err := e.Submit("generate", r)
	if err != nil {
		t.Fatal(err)
	}
	var payload, document []byte
	if err = e.db.QueryRow("SELECT payload,document FROM tasks WHERE id=?", task.ID).Scan(&payload, &document); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(oldSecret)) || bytes.Contains(document, []byte(oldSecret)) {
		t.Fatal("inline token was persisted in task data")
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	m.Token = newSecret
	cfg.Models["demo"] = m
	e2 := startEngine(t, cfg)
	completed := waitTask(t, e2, task.ID)
	if completed.Status != "succeeded" {
		t.Fatalf("recovery failed: %+v", completed)
	}
	again, err := e2.Submit("generate", r)
	if err != nil || again.ID != task.ID || calls.Load() != 1 {
		t.Fatal("credential rotation broke deduplication")
	}
}

func TestQueuedCredentialsCannotMoveToAnotherUpstream(t *testing.T) {
	cfg := testConfig(t, "https://original.example/v1")
	m := cfg.Models["demo"]
	m.Token = "inline-secret"
	m.TokenEnv = ""
	cfg.Models["demo"] = m
	snapshot := cloneJSON(m)
	for _, change := range []string{"provider", "base_url", "model"} {
		changed := m
		switch change {
		case "provider":
			changed.Provider = "openai-compatible"
		case "base_url":
			changed.BaseURL = "https://different.example/v1"
		case "model":
			changed.Model = "different-model"
		}
		cfg.Models["demo"] = changed
		if _, err := cfg.taskCredential("demo", snapshot); err == nil {
			t.Fatalf("changed %s reused credentials on saved request", change)
		}
	}
}
