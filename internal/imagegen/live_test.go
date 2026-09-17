package imagegen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// TestLiveProviders is opt-in because these calls use real paid services. A
// caller-supplied run ID and persistent task database make reruns idempotent.
// Never use t.TempDir for its outputs or automatically retry an unknown task.
func TestLiveProviders(t *testing.T) {
	configPath := os.Getenv("IMAGE_MCP_LIVE_CONFIG")
	if configPath == "" {
		t.Skip("real-provider acceptance not run: IMAGE_MCP_LIVE_CONFIG is unset")
	}
	runID := os.Getenv("IMAGE_MCP_LIVE_RUN_ID")
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(runID) {
		t.Fatal("set IMAGE_MCP_LIVE_RUN_ID to a stable 1..64 character identifier (letters, digits, underscore or hyphen); reusing it avoids repeat charges")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{}
	if selected := os.Getenv("IMAGE_MCP_LIVE_MODELS"); selected != "" {
		for _, alias := range strings.Split(selected, ",") {
			aliases = append(aliases, strings.TrimSpace(alias))
		}
	} else {
		for alias := range cfg.Models {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	aliases = slices.Compact(aliases)
	// Preflight all selected profiles before the first paid request.
	for _, alias := range aliases {
		model, ok := cfg.Models[alias]
		if !ok {
			t.Fatalf("unknown live model alias %q", alias)
		}
		if model.credential() == "" || strings.HasPrefix(model.credential(), "YOUR_") {
			t.Fatalf("model %q has no usable credential; configure token or its token_env variable", alias)
		}
		if strings.HasPrefix(model.Model, "YOUR_") || strings.Contains(model.BaseURL, "YOUR_") {
			t.Fatalf("replace placeholder configuration for %q", alias)
		}
	}
	cfg.OutputDir = filepath.Join(cfg.OutputDir, "live-verification")
	cfg.AllowedInputDirs = append(cfg.AllowedInputDirs, filepath.Join(cfg.OutputDir, "images"))
	cfg.WaitTimeout, cfg.Retention = "0s", "0s"
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	session, err := NewMCPServer(engine, "stdio").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "image-gen-live-verifier", Version: Version}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			model := cfg.Models[alias]
			reference := liveReferencePNG(t)
			if slices.Contains(model.Capabilities, "generate") {
				task := liveTask(t, client, cfg, "generate_image", Request{Model: alias, Prompt: "A simple red square centered on a plain white background, no text.", N: 1, RequestID: "live:" + runID + ":" + aliasPath(alias) + ":generate"})
				data, err := os.ReadFile(task.Assets[0].Path)
				if err != nil {
					t.Fatal(err)
				}
				im, _, err := image.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				var buf bytes.Buffer
				if err = png.Encode(&buf, im); err != nil {
					t.Fatal(err)
				}
				reference = buf.Bytes()
			}
			if !slices.Contains(model.Capabilities, "edit") {
				return
			}
			// Stable paths preserve request fingerprints across repeated executions.
			prefix := filepath.Join(cfg.OutputDir, "images", "live-inputs", runID, aliasPath(alias))
			if err := os.MkdirAll(prefix, 0700); err != nil {
				t.Fatal(err)
			}
			referencePath := filepath.Join(prefix, "reference.png")
			if err := atomicWrite(referencePath, reference); err != nil {
				t.Fatal(err)
			}
			liveTask(t, client, cfg, "edit_image", Request{Model: alias, Prompt: "Change the red square into a blue circle. Keep the plain white background.", Images: []ImageInput{{Path: referencePath}}, N: 1, RequestID: "live:" + runID + ":" + aliasPath(alias) + ":edit"})
			if !slices.Contains(model.Capabilities, "mask") {
				return
			}
			info, _, err := image.DecodeConfig(bytes.NewReader(reference))
			if err != nil {
				t.Fatal(err)
			}
			mask := image.NewNRGBA(image.Rect(0, 0, info.Width, info.Height))
			for y := 0; y < info.Height; y++ {
				for x := 0; x < info.Width; x++ {
					if x < info.Width/4 || x >= 3*info.Width/4 || y < info.Height/4 || y >= 3*info.Height/4 {
						mask.SetNRGBA(x, y, color.NRGBA{A: 255})
					}
				}
			}
			var maskData bytes.Buffer
			if err = png.Encode(&maskData, mask); err != nil {
				t.Fatal(err)
			}
			maskPath := filepath.Join(prefix, "mask.png")
			if err = atomicWrite(maskPath, maskData.Bytes()); err != nil {
				t.Fatal(err)
			}
			liveTask(t, client, cfg, "edit_image", Request{Model: alias, Prompt: "Replace the object in the transparent masked region with a small green star.", Images: []ImageInput{{Path: referencePath}}, Mask: &ImageInput{Path: maskPath}, N: 1, RequestID: "live:" + runID + ":" + aliasPath(alias) + ":mask"})
		})
	}
}

func aliasPath(alias string) string {
	// An alias is arbitrary configuration text, so never use it as a directory.
	hash := sha256.Sum256([]byte(alias))
	return hex.EncodeToString(hash[:])
}

func liveReferencePNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			pixel := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			if x >= 256 && x < 768 && y >= 256 && y < 768 {
				pixel = color.NRGBA{R: 255, A: 255}
			}
			im.SetNRGBA(x, y, pixel)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLiveVerifierRerunDoesNotResubmit(t *testing.T) {
	data := fixturePNG(t, false)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); respondImage(w, data) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	configPath := filepath.Join(t.TempDir(), "live.yaml")
	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGE_MCP_LIVE_CONFIG", configPath)
	t.Setenv("IMAGE_MCP_LIVE_MODELS", "demo")
	t.Setenv("IMAGE_MCP_LIVE_RUN_ID", "rerun-test")
	TestLiveProviders(t)
	if calls.Load() != 3 {
		t.Fatalf("first run made %d calls, want generate/edit/mask", calls.Load())
	}
	TestLiveProviders(t)
	if calls.Load() != 3 {
		t.Fatalf("verification rerun resubmitted requests: %d calls", calls.Load())
	}
}

func liveTask(t *testing.T, client *mcp.ClientSession, cfg Config, tool string, args Request) Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration(cfg.TaskTimeout)+time.Minute)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var task Task
	for {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &task); err != nil || task.ID == "" {
			// Input/configuration errors have text-only tool results. Avoid printing
			// the whole response, which may echo the original arguments.
			t.Fatalf("%s did not produce a task (isError=%v); inspect model capabilities and configuration", tool, result.IsError)
		}
		if terminal(task.Status) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("task %s still %s; preserve run ID and query it later", task.ID, task.Status)
		case <-time.After(time.Second):
		}
		result, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "get_image_task", Arguments: taskQuery{TaskID: task.ID}})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("provider=%s alias=%s operation=%s task=%s status=%s", cfg.Models[args.Model].Provider, args.Model, tool, task.ID, task.Status)
	if task.Status != "succeeded" || result.IsError || len(task.Assets) != 1 {
		t.Fatalf("task %s failed: status=%s error=%v; do not retry with a new run ID until the outcome is understood", task.ID, task.Status, task.Error)
	}
	hasImage := false
	for _, content := range result.Content {
		if preview, ok := content.(*mcp.ImageContent); ok {
			hasImage = true
			if _, _, err := image.DecodeConfig(bytes.NewReader(preview.Data)); err != nil {
				t.Fatal("invalid preview")
			}
		}
	}
	if !hasImage {
		t.Fatal("completed live task lacks an inline preview")
	}
	for _, asset := range task.Assets {
		if _, err := os.Stat(asset.Path); err != nil {
			t.Fatal(err)
		}
		t.Logf("original image: %s (%s, %dx%d)", asset.Path, asset.MIME, asset.Width, asset.Height)
	}
	return task
}
