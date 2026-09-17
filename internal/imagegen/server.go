package imagegen

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is set from the release tag by the packaging script.
var Version = "dev"

type taskQuery struct {
	TaskID string `json:"task_id" jsonschema:"Task ID returned by generation or editing"`
}
type modelInfo struct {
	Alias          string               `json:"alias"`
	Provider       string               `json:"provider"`
	Model          string               `json:"model"`
	Capabilities   []string             `json:"capabilities"`
	Parameters     map[string][]string  `json:"parameters,omitempty"`
	Extensions     map[string]Extension `json:"extensions,omitempty"`
	MaxImages      int                  `json:"max_images"`
	MaxInputImages int                  `json:"max_input_images"`
	Configured     bool                 `json:"token_configured"`
}
type modelList struct {
	Default string      `json:"default_model"`
	Models  []modelInfo `json:"models"`
}

func NewMCPServer(e *Engine, transport string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "image-gen-mcp", Version: Version}, &mcp.ServerOptions{Instructions: "Use list_image_models before choosing model parameters. Generate/edit may return a task_id: poll get_image_task until terminal. Always reuse request_id on a repeated submission. Never automatically resubmit an unknown task: it may already have been billed. Images in tool results are previews; metadata points to full originals. Local image paths refer to the MCP server."})
	mcp.AddTool(s, &mcp.Tool{Name: "list_image_models", Description: "List configured aliases, capabilities and parameter values. An empty parameter value list permits any string; unsupported parameters are rejected."}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, modelList, error) {
		out := modelList{Default: e.cfg.DefaultModel, Models: []modelInfo{}}
		for alias, m := range e.cfg.Models {
			out.Models = append(out.Models, modelInfo{Alias: alias, Provider: m.Provider, Model: m.Model, Capabilities: m.Capabilities, Parameters: m.Parameters, Extensions: m.Extensions, MaxImages: m.MaxImages, MaxInputImages: m.MaxInputImages, Configured: m.credential() != ""})
		}
		sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Alias < out.Models[j].Alias })
		return nil, out, nil
	})
	for _, operation := range []string{"generate", "edit"} {
		description := "Generate images from text. May incur charges. Returns a completed task with preview images or a task_id to poll."
		if operation == "edit" {
			description = "Generate from reference images or edit existing images with a prompt and optional PNG mask. Transparent mask pixels mark edits to the first image. Mask boundaries guide the provider; inspect results because unmasked regions may also change. May incur charges. Returns images or a task_id to poll."
		}
		mcp.AddTool(s, &mcp.Tool{Name: operation + "_image", Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, r Request) (*mcp.CallToolResult, Task, error) {
			t, err := e.Submit(operation, r)
			if err != nil {
				return nil, Task{}, err
			}
			t, err = e.Wait(ctx, t.ID, duration(e.cfg.WaitTimeout))
			if err != nil {
				return nil, Task{}, err
			}
			return e.result(t, transport)
		})
	}
	mcp.AddTool(s, &mcp.Tool{Name: "get_image_task", Description: "Query a task. Completed tasks include preview images and original file metadata. unknown means generation may have completed and must not be retried automatically."}, func(_ context.Context, _ *mcp.CallToolRequest, q taskQuery) (*mcp.CallToolResult, Task, error) {
		t, err := e.Get(q.TaskID)
		if err != nil {
			return nil, Task{}, err
		}
		return e.result(t, transport)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "cancel_image_task", Description: "Cancel queued work or local waiting. If already submitted, upstream may continue and charge; the task becomes unknown."}, func(_ context.Context, _ *mcp.CallToolRequest, q taskQuery) (*mcp.CallToolResult, Task, error) {
		t, err := e.Cancel(q.TaskID)
		if err != nil {
			return nil, Task{}, err
		}
		return e.result(t, transport)
	})
	return s
}
func (e *Engine) result(t Task, transport string) (*mcp.CallToolResult, Task, error) {
	t = cloneJSON(t)
	for i, a := range t.Assets {
		if transport == "stdio" {
			t.Assets[i].Path = filepath.Join(e.cfg.OutputDir, "images", t.ID, a.Name)
		} else {
			t.Assets[i].URL = strings.TrimRight(e.cfg.HTTP.PublicURL, "/") + "/images/" + t.ID + "/" + a.Name
			t.DownloadRequiresAuth = os.Getenv(e.cfg.HTTP.TokenEnv) != ""
		}
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, Task{}, err
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: t.Status == "failed" || t.Status == "unknown"}
	for _, a := range t.Assets {
		path := filepath.Join(e.cfg.OutputDir, "images", t.ID, a.Name+".preview.png")
		preview, err := os.ReadFile(path)
		if err != nil {
			result.Content = append(result.Content, &mcp.TextContent{Text: "Preview unavailable; the image may have expired or been removed."})
			continue
		}
		result.Content = append(result.Content, &mcp.ImageContent{Data: preview, MIMEType: "image/png"})
	}
	return result, t, nil
}
func NewHTTPHandler(e *Engine) (http.Handler, error) {
	if err := e.cfg.ValidateHTTP(); err != nil {
		return nil, err
	}
	public, _ := url.Parse(e.cfg.HTTP.PublicURL)
	s := NewMCPServer(e, "http")
	// Host and Origin are checked below, including when behind a reverse proxy.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("GET /images/{task}/{name}", func(w http.ResponseWriter, r *http.Request) {
		t, err := e.Get(r.PathValue("task"))
		if err != nil || t.Status != "succeeded" || (t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now())) {
			http.NotFound(w, r)
			return
		}
		var asset *Asset
		for _, a := range t.Assets {
			if a.Name == r.PathValue("name") {
				asset = &a
				break
			}
		}
		if asset == nil {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(filepath.Join(e.cfg.OutputDir, "images"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		f, err := root.Open(filepath.Join(t.ID, asset.Name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", asset.MIME)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, asset.Name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, asset.Name, info.ModTime(), f)
	})
	token := os.Getenv(e.cfg.HTTP.TokenEnv)
	expected := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, public.Host) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != public.Scheme+"://"+public.Host {
			http.Error(w, "unexpected Origin", http.StatusForbidden)
			return
		}
		if token != "" {
			actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
			if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="image-gen-mcp"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, min(int64(512<<20), e.cfg.MaxImageBytes*17*4/3+(1<<20)))
		mux.ServeHTTP(w, r)
	}), nil
}
