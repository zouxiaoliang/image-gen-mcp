// The demo runs a real MCP subprocess against a local fixture replay server.
// It never reads config.yaml or contacts an external image provider.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/image-gen-mcp/internal/imagegen"
	"gopkg.in/yaml.v3"
)

type event struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Image  string `json:"image,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	serverPath := flag.String("server", "./bin/image-gen-mcp", "MCP executable")
	assets := flag.String("assets", "docs/assets", "Directory containing generated.png and edited.png")
	record := flag.String("record", "", "Optional JSON recording for the GIF renderer")
	flag.Parse()
	server, err := filepath.Abs(*serverPath)
	if err != nil {
		return err
	}
	generated, err := os.ReadFile(filepath.Join(*assets, "generated.png"))
	if err != nil {
		return err
	}
	edited, err := os.ReadFile(filepath.Join(*assets, "edited.png"))
	if err != nil {
		return err
	}
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []byte
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations":
			data = generated
		case r.Method == http.MethodPost && r.URL.Path == "/v1/images/edits":
			data = edited
		default:
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(data)}},
		})
	}))
	defer upstream.Close()
	dir, err := os.MkdirTemp("", "image-gen-mcp-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cfg := map[string]any{
		"default_model": "demo", "output_dir": filepath.Join(dir, "output"),
		"allowed_input_dirs": []string{filepath.Join(dir, "output", "images")},
		"wait_timeout":       "0s",
		"models": map[string]any{"demo": map[string]any{
			"provider": "openai-compatible", "base_url": upstream.URL + "/v1",
			"token": "local-demo-only", "model": "fixture-replay",
			"capabilities": []string{"generate", "edit"}, "max_images": 1, "max_input_images": 1,
		}},
	}
	config, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configPath, config, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, server, "-config", configPath, "-transport", "stdio")
	cmd.Stderr = os.Stderr
	session, err := mcp.NewClient(&mcp.Implementation{Name: "demo", Version: "1"}, nil).Connect(
		ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	events := []event{}
	log := func(title, detail, image string) {
		events = append(events, event{title, detail, image})
		fmt.Printf("%s\n  %s\n", title, detail)
	}
	log("Connect over stdio", "Real MCP subprocess / local fixture replay / no API key", "")
	toolList, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	if len(toolList.Tools) != 5 {
		return fmt.Errorf("expected 5 tools, received %d", len(toolList.Tools))
	}
	models, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_image_models", Arguments: map[string]any{}})
	if err != nil {
		return err
	}
	if models.IsError {
		return fmt.Errorf("list_image_models failed")
	}
	log("Discover tools and models", "5 tools available / demo model configured", "")
	args := map[string]any{"prompt": "A red square on a white background", "request_id": "demo-generate"}
	first, err := task(ctx, session, "generate_image", args, generated)
	if err != nil {
		return err
	}
	log("generate_image -> get_image_task", "succeeded / PNG preview + original file verified", "generated.png")
	_, err = task(ctx, session, "edit_image", map[string]any{
		"prompt": "Turn the red square into a blue circle; keep the white background",
		"images": []map[string]string{{"path": first.Assets[0].Path}}, "request_id": "demo-edit",
	}, edited)
	if err != nil {
		return err
	}
	log("edit_image -> get_image_task", "succeeded / reference image supplied / original verified", "edited.png")
	repeated, err := task(ctx, session, "generate_image", args, generated)
	if err != nil {
		return err
	}
	if repeated.ID != first.ID || requests.Load() != 2 {
		return fmt.Errorf("deduplication failed: %d upstream requests", requests.Load())
	}
	log("Repeat the same request_id", "Same task returned / 2 upstream calls total / no duplicate", "edited.png")
	if *record != "" {
		data, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(*record, append(data, '\n'), 0644)
	}
	return nil
}

func task(ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any, expected []byte) (imagegen.Task, error) {
	var out imagegen.Task
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	for err == nil {
		if result.IsError {
			return out, fmt.Errorf("%s returned an MCP error", tool)
		}
		encoded, marshalErr := json.Marshal(result.StructuredContent)
		if marshalErr != nil {
			return out, marshalErr
		}
		if err = json.Unmarshal(encoded, &out); err != nil {
			return out, err
		}
		switch out.Status {
		case "succeeded":
			if len(out.Assets) != 1 {
				return out, fmt.Errorf("expected one original image")
			}
			data, err := os.ReadFile(out.Assets[0].Path)
			if err != nil {
				return out, err
			}
			if !bytes.Equal(data, expected) {
				return out, fmt.Errorf("original image differs from the fixture")
			}
			for _, content := range result.Content {
				if im, ok := content.(*mcp.ImageContent); ok && im.MIMEType == "image/png" && bytes.HasPrefix(im.Data, []byte("\x89PNG")) {
					return out, nil
				}
			}
			return out, fmt.Errorf("PNG preview missing")
		case "queued", "preparing", "running":
		case "":
			return out, fmt.Errorf("task status missing")
		default:
			return out, fmt.Errorf("task ended with status %s", out.Status)
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_image_task", Arguments: map[string]any{"task_id": out.ID}})
	}
	return out, err
}
