package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectTestMCP(t *testing.T, e *Engine) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := NewMCPServer(e, "stdio").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance-agent", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
func callTaskTool(t *testing.T, client *mcp.ClientSession, name string, args any) (Task, *mcp.CallToolResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var task Task
	if err = json.Unmarshal(b, &task); err != nil || task.ID == "" {
		t.Fatalf("missing structured task: %s; error=%v; result=%+v", b, err, result)
	}
	return task, result
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEditWorkflowsThroughMCP(t *testing.T) {
	for _, provider := range []string{"openai", "openai-compatible", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			original, mask := fixturePNG(t, false), fixturePNG(t, true)
			var submissions, masked atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				submissions.Add(1)
				if provider == "gemini" {
					if r.URL.Path != "/v1beta/models/test-model:generateContent" {
						t.Errorf("wrong Gemini path: %s", r.URL.Path)
					}
					var body struct {
						Contents []struct {
							Parts []struct {
								Inline *struct {
									Data string `json:"data"`
									MIME string `json:"mimeType"`
								} `json:"inlineData"`
							} `json:"parts"`
						} `json:"contents"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if len(body.Contents) != 1 || len(body.Contents[0].Parts) != 2 {
						t.Error("missing reference image")
						return
					}
					part := body.Contents[0].Parts[1].Inline
					if part == nil || part.MIME != "image/png" || part.Data != base64.StdEncoding.EncodeToString(original) {
						t.Error("reference was not forwarded intact")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(original), "mimeType": "image/png"}}}}}}})
					return
				}
				if r.URL.Path != "/v1/images/edits" {
					t.Errorf("wrong edit path: %s", r.URL.Path)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				f, _, err := r.FormFile("image")
				if err != nil {
					t.Error(err)
					return
				}
				data, _ := io.ReadAll(f)
				f.Close()
				if !bytes.Equal(data, original) {
					t.Error("reference was not forwarded intact")
				}
				if masks := r.MultipartForm.File["mask"]; len(masks) > 0 {
					masked.Add(1)
					f, err := masks[0].Open()
					if err != nil {
						t.Error(err)
						return
					}
					data, _ = io.ReadAll(f)
					f.Close()
					if !bytes.Equal(data, mask) {
						t.Error("mask was not forwarded intact")
					}
				}
				respondImage(w, original)
			}))
			t.Cleanup(upstream.Close)
			cfg := testConfig(t, upstream.URL+"/v1")
			m := cfg.Models["demo"]
			m.Provider = provider
			if provider == "gemini" {
				m.BaseURL = upstream.URL + "/v1beta"
				m.Capabilities = []string{"generate", "edit"}
				m.MaxImages = 1
				m.Parameters = nil
			}
			cfg.Models["demo"] = m
			cfg.AllowedInputDirs = []string{t.TempDir()}
			path := filepath.Join(cfg.AllowedInputDirs[0], "input.png")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			e := startEngine(t, cfg)
			// A public URL fixture supplies image bytes without internet access.
			// Other tests exercise private-address rejection in the production dialer.
			e.images.fetch = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://image.example/reference.png" {
					t.Errorf("unexpected input URL: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("credential leaked during image download")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(original)), Header: make(http.Header)}, nil
			})}
			client := connectTestMCP(t, e)
			for _, in := range []ImageInput{{Path: path}, {Data: base64.StdEncoding.EncodeToString(original)}, {URL: "https://image.example/reference.png"}} {
				task, result := callTaskTool(t, client, "edit_image", Request{Prompt: "Make the background blue", Images: []ImageInput{in}, RequestID: randomID()})
				if result.IsError || task.Status != "succeeded" || len(task.Assets) != 1 {
					t.Fatalf("edit failed: %+v", task)
				}
				got, err := os.ReadFile(task.Assets[0].Path)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatalf("missing original output: %v", err)
				}
				if len(result.Content) != 2 {
					t.Fatal("edit did not include a preview")
				}
			}
			args := Request{Prompt: "Replace the masked background", Images: []ImageInput{{Path: path}}, Mask: &ImageInput{Data: base64.StdEncoding.EncodeToString(mask)}, RequestID: randomID()}
			if provider == "gemini" {
				res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "edit_image", Arguments: args})
				if err != nil || !res.IsError {
					t.Fatalf("unsupported mask not rejected: %+v %v", res, err)
				}
				if submissions.Load() != 3 {
					t.Fatal("unsupported mask reached paid upstream")
				}
			} else {
				task, res := callTaskTool(t, client, "edit_image", args)
				if res.IsError || task.Status != "succeeded" || masked.Load() != 1 || submissions.Load() != 4 {
					t.Fatalf("mask edit failed: %+v", task)
				}
			}
		})
	}
}

func TestModelCatalogEmptyParameterValues(t *testing.T) {
	cfg := testConfig(t, "http://localhost/v1")
	m := cfg.Models["demo"]
	m.Parameters = map[string][]string{"size": nil}
	cfg.Models["demo"] = m
	client := connectTestMCP(t, startEngine(t, cfg))
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_image_models", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("catalog cannot represent unrestricted parameter values: %+v %v", res, err)
	}
}

func TestMCPErrorResultsRemainStructured(t *testing.T) {
	for _, status := range []int{400, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			t.Cleanup(upstream.Close)
			e := startEngine(t, testConfig(t, upstream.URL))
			client := connectTestMCP(t, e)
			task, res := callTaskTool(t, client, "generate_image", Request{Prompt: "x", RequestID: "error-task"})
			want := "failed"
			if status == 500 {
				want = "unknown"
			}
			if task.Status != want || !res.IsError || task.Error == nil {
				t.Fatalf("incorrect failure metadata: %+v", task)
			}
			queried, res := callTaskTool(t, client, "get_image_task", taskQuery{TaskID: task.ID})
			if !res.IsError || queried.ID != task.ID || queried.Status != want {
				t.Fatalf("lost failure state: %+v", queried)
			}
		})
	}
}
