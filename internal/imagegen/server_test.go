package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type authTransport struct{ base http.RoundTripper }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer mcp-test-secret")
	return a.base.RoundTrip(r)
}
func exerciseMCP(t *testing.T, session *mcp.ClientSession) Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 5 {
		t.Fatalf("got %d tools", len(list.Tools))
	}
	models, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_image_models", Arguments: map[string]any{}})
	if err != nil || models.IsError {
		t.Fatalf("models error: %v %+v", err, models)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "hello", "request_id": "mcp-call"}})
	if err != nil {
		t.Fatal(err)
	}
	var task Task
	b, _ := json.Marshal(result.StructuredContent)
	if err = json.Unmarshal(b, &task); err != nil {
		t.Fatal(err)
	}
	for !terminal(task.Status) {
		select {
		case <-ctx.Done():
			t.Fatal("task timed out")
		case <-time.After(20 * time.Millisecond):
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_image_task", Arguments: map[string]any{"task_id": task.ID}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(result.StructuredContent)
		if err = json.Unmarshal(b, &task); err != nil {
			t.Fatal(err)
		}
	}
	if result.IsError || task.Status != "succeeded" || len(task.Assets) != 1 {
		t.Fatalf("generation failed: %+v %+v", task, result)
	}
	var hasImage bool
	for _, content := range result.Content {
		if im, ok := content.(*mcp.ImageContent); ok {
			hasImage = true
			if im.MIMEType != "image/png" || !bytes.HasPrefix(im.Data, []byte("\x89PNG")) {
				t.Fatal("invalid embedded image bytes")
			}
		}
	}
	if !hasImage {
		t.Fatal("preview not included")
	}
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "x", "unknown_field": true}})
	if err == nil && !bad.IsError {
		t.Fatal("unknown tool parameter accepted")
	}
	return task
}
func TestMCPOverHTTP(t *testing.T) {
	data := fixturePNG(t, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respondImage(w, data) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	t.Setenv("TEST_MCP_TOKEN", "mcp-test-secret")
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()
	cfg.HTTP = HTTPConfig{Listen: srv.Listener.Addr().String(), PublicURL: "http://" + srv.Listener.Addr().String(), TokenEnv: "TEST_MCP_TOKEN"}
	e := startEngine(t, cfg)
	handler, err := NewHTTPHandler(e)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = handler
	srv.Start()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: authTransport{http.DefaultTransport}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	task := exerciseMCP(t, session)
	asset := task.Assets[0]
	if asset.Path != "" || asset.URL == "" || !task.DownloadRequiresAuth {
		t.Fatal("HTTP result should expose authenticated URL, not server path")
	}
	resp, err := http.Get(asset.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unprotected image download: %d", resp.StatusCode)
	}
	authClient := http.Client{Transport: authTransport{http.DefaultTransport}}
	resp, err = authClient.Get(asset.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := boundedRead(resp.Body, 1<<20)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("download did not return original")
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Header.Set("Origin", "https://untrusted.example")
	resp, err = authClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("foreign Origin accepted")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Host = "untrusted.example"
	resp, err = authClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("foreign Host accepted")
	}
}

// A subprocess exercises the real SDK StdioTransport, including EOF shutdown.
func TestStdioHelper(t *testing.T) {
	if os.Getenv("IMAGE_MCP_STDIO_HELPER") != "1" {
		return
	}
	var cfg Config
	if err := json.Unmarshal([]byte(os.Getenv("IMAGE_MCP_TEST_CONFIG")), &cfg); err != nil {
		os.Exit(2)
	}
	e, err := NewEngine(cfg)
	if err != nil {
		os.Exit(3)
	}
	err = NewMCPServer(e, "stdio").Run(context.Background(), &mcp.StdioTransport{})
	_ = e.Close()
	if err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
func TestMCPOverStdio(t *testing.T) {
	data := fixturePNG(t, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respondImage(w, data) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	b, _ := json.Marshal(cfg)
	cmd := exec.Command(os.Args[0], "-test.run=^TestStdioHelper$")
	cmd.Env = append(os.Environ(), "IMAGE_MCP_STDIO_HELPER=1", "IMAGE_MCP_TEST_CONFIG="+string(b))
	cmd.Stderr = os.Stderr
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	task := exerciseMCP(t, session)
	if task.Assets[0].Path == "" || task.Assets[0].URL != "" {
		t.Fatal("stdio result should expose local path")
	}
	if _, err = os.Stat(task.Assets[0].Path); err != nil {
		t.Fatal(err)
	}
}
