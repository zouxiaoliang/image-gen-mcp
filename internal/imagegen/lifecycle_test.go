package imagegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPTaskSurvivesClientDisconnect(t *testing.T) {
	data := fixturePNG(t, false)
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			respondImage(w, data)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	cfg := testConfig(t, upstream.URL)
	cfg.WaitTimeout = "0s"
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	cfg.HTTP = HTTPConfig{Listen: server.Listener.Addr().String(), PublicURL: "http://" + server.Listener.Addr().String()}
	e := startEngine(t, cfg)
	handler, err := NewHTTPHandler(e)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	connect := func() *mcp.ClientSession {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		client, err := mcp.NewClient(&mcp.Implementation{Name: "disconnect-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	client := connect()
	task, _ := callTaskTool(t, client, "generate_image", Request{Prompt: "background job", RequestID: "disconnect-key"})
	if terminal(task.Status) {
		t.Fatalf("generation returned terminal before upstream finished: %+v", task)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation not started")
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stillRunning, err := e.Get(task.ID)
	if err != nil || stillRunning.Status != "running" {
		t.Fatalf("disconnect cancelled background task: %+v %v", stillRunning, err)
	}
	close(release)
	completed := waitTask(t, e, task.ID)
	if completed.Status != "succeeded" {
		t.Fatalf("background completion failed: %+v", completed)
	}
	newClient := connect()
	got, result := callTaskTool(t, newClient, "get_image_task", taskQuery{TaskID: task.ID})
	if result.IsError || got.ID != task.ID || got.Status != "succeeded" || len(result.Content) != 2 {
		t.Fatalf("new client cannot retrieve result: %+v", got)
	}
}

func TestCrashRecoveryOverStdio(t *testing.T) {
	data := fixturePNG(t, false)
	started := make(chan struct{})
	release := make(chan struct{})
	var submitted, queued atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Prompt == "already submitted" {
			if submitted.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		queued.Add(1)
		respondImage(w, data)
	}))
	defer upstream.Close()
	defer close(release)
	cfg := testConfig(t, upstream.URL)
	cfg.WaitTimeout = "0s"
	encoded, _ := json.Marshal(cfg)
	cmd := exec.Command(os.Args[0], "-test.run=^TestStdioHelper$")
	cmd.Env = append(os.Environ(), "IMAGE_MCP_STDIO_HELPER=1", "IMAGE_MCP_TEST_CONFIG="+string(encoded))
	cmd.Stderr = os.Stderr
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "crash-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	firstRequest := Request{Prompt: "already submitted", RequestID: "submitted-before-crash"}
	first, _ := callTaskTool(t, client, "generate_image", firstRequest)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first task never reached upstream")
	}
	second, _ := callTaskTool(t, client, "generate_image", Request{Prompt: "queued before crash", RequestID: "queued-before-crash"})
	if second.Status != "queued" {
		t.Fatalf("expected a durable queued task, got %s", second.Status)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// CommandTransport waits for the child process when closing its pipes.
	_ = client.Close()
	e := startEngine(t, cfg)
	unknownTask, err := e.Get(first.ID)
	if err != nil || unknownTask.Status != "unknown" {
		t.Fatalf("submitted task not marked unknown after crash: %+v %v", unknownTask, err)
	}
	completed := waitTask(t, e, second.ID)
	if completed.Status != "succeeded" {
		t.Fatalf("queued task not recovered: %+v", completed)
	}
	duplicate, err := e.Submit("generate", firstRequest)
	if err != nil || duplicate.ID != first.ID || duplicate.Status != "unknown" {
		t.Fatalf("crashed task lost deduplication: %+v %v", duplicate, err)
	}
	if submitted.Load() != 1 || queued.Load() != 1 {
		t.Fatalf("crash caused duplicate/omitted generation: submitted=%d queued=%d", submitted.Load(), queued.Load())
	}
}
