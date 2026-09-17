package imagegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func startEngine(t *testing.T, c Config) *Engine {
	t.Helper()
	e, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	return e
}
func waitTask(t *testing.T, e *Engine, id string) Task {
	t.Helper()
	task, err := e.Wait(context.Background(), id, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !terminal(task.Status) {
		t.Fatalf("task did not finish: %+v", task)
	}
	return task
}
func TestConcurrentDeduplicationAndRestart(t *testing.T) {
	data := fixturePNG(t, false)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); respondImage(w, data) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	e := startEngine(t, cfg)
	req := Request{Prompt: "hello", RequestID: "stable-id"}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := e.Submit("generate", req)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- task.ID
		}()
	}
	wg.Wait()
	close(ids)
	var id string
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate task")
		}
		id = got
	}
	task := waitTask(t, e, id)
	if task.Status != "succeeded" || calls.Load() != 1 || len(task.Assets) != 1 {
		t.Fatalf("unexpected task/calls: %+v/%d", task, calls.Load())
	}
	if _, err := NewEngine(cfg); err == nil {
		t.Fatal("second server accepted same output directory")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := startEngine(t, cfg)
	again, err := e2.Submit("generate", req)
	if err != nil || again.ID != id || again.Status != "succeeded" {
		t.Fatalf("restart dedup failed: %+v %v", again, err)
	}
	req.Prompt = "different"
	if _, err = e2.Submit("generate", req); err == nil {
		t.Fatal("conflicting id accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("resubmitted to upstream")
	}
}
func TestQueueAndCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	e := startEngine(t, testConfig(t, upstream.URL))
	one, err := e.Submit("generate", Request{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not started")
	}
	two, err := e.Submit("generate", Request{Prompt: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit("generate", Request{Prompt: "three"}); err == nil {
		t.Fatal("full queue accepted request")
	}
	q, err := e.Cancel(two.ID)
	if err != nil || q.Status != "cancelled" {
		t.Fatalf("queued cancellation: %+v %v", q, err)
	}
	r, err := e.Cancel(one.ID)
	if err != nil || r.Status != "unknown" || !r.Error.ResultUnknown {
		t.Fatalf("running cancellation: %+v %v", r, err)
	}
}
func TestUnknownOutcomesAreNotRetried(t *testing.T) {
	for _, status := range []int{500, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer upstream.Close()
			e := startEngine(t, testConfig(t, upstream.URL))
			task, err := e.Submit("generate", Request{Prompt: "x", RequestID: "same"})
			if err != nil {
				t.Fatal(err)
			}
			task = waitTask(t, e, task.ID)
			want := "failed"
			if status == 500 {
				want = "unknown"
			}
			if task.Status != want {
				t.Fatalf("got %s want %s", task.Status, want)
			}
			_, err = e.Submit("generate", Request{Prompt: "x", RequestID: "same"})
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatal("generation was retried")
			}
		})
	}
}

func TestInterruptedGenerationIsUnknown(t *testing.T) {
	for _, mode := range []string{"disconnect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "disconnect" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer upstream.Close()
			defer close(release)
			cfg := testConfig(t, upstream.URL)
			cfg.TaskTimeout = "100ms"
			e := startEngine(t, cfg)
			task, err := e.Submit("generate", Request{Prompt: "x", RequestID: "interrupted"})
			if err != nil {
				t.Fatal(err)
			}
			task = waitTask(t, e, task.ID)
			if task.Status != "unknown" || calls.Load() != 1 {
				t.Fatalf("unexpected result: %+v calls=%d", task, calls.Load())
			}
		})
	}
}
func TestRestartMarksSubmittedTaskUnknown(t *testing.T) {
	cfg := testConfig(t, "http://localhost:1/v1")
	e := startEngine(t, cfg)
	task := Task{ID: randomID(), Status: "running", Model: "demo", Operation: "generate", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	doc, _ := json.Marshal(task)
	e.mu.Lock()
	_, err := e.db.Exec("INSERT INTO tasks VALUES(?,?,?,?,?,?,?)", task.ID, "crashed-id", "hash", "running", time.Now().Format(time.RFC3339Nano), doc, []byte("{}"))
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := startEngine(t, cfg)
	got, err := e2.Get(task.ID)
	if err != nil || got.Status != "unknown" || !got.Error.ResultUnknown {
		t.Fatalf("recovery: %+v %v", got, err)
	}
}
func TestQueuedTaskSurvivesRestart(t *testing.T) {
	data := fixturePNG(t, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respondImage(w, data) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	e := startEngine(t, cfg)
	// Stop workers before inserting a durable queue entry, simulating a crash
	// before a worker claims it. Reopening must execute it exactly once.
	e.stop()
	e.wg.Wait()
	task := Task{ID: randomID(), Status: "queued", Model: "demo", Operation: "generate", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	doc, _ := json.Marshal(task)
	payload, _ := json.Marshal(job{Operation: "generate", Request: Request{Model: "demo", Prompt: "x", N: 1}, Model: cfg.Models["demo"]})
	if _, err := e.db.Exec("INSERT INTO tasks VALUES(?,?,?,?,?,?,?)", task.ID, nil, "hash", "queued", time.Now().Format(time.RFC3339Nano), doc, payload); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := startEngine(t, cfg)
	if got := waitTask(t, e2, task.ID); got.Status != "succeeded" {
		t.Fatalf("queue recovery: %+v", got)
	}
}
func TestRetentionKeepsDeduplicationRecord(t *testing.T) {
	data := fixturePNG(t, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respondImage(w, data) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL)
	cfg.Retention = "1h"
	e := startEngine(t, cfg)
	r := Request{Prompt: "x", RequestID: "expired-key"}
	task, err := e.Submit("generate", r)
	if err != nil {
		t.Fatal(err)
	}
	task = waitTask(t, e, task.ID)
	if task.ExpiresAt == nil {
		t.Fatal("missing expiry")
	}
	past := time.Now().Add(-time.Hour)
	task.ExpiresAt = &past
	e.mu.Lock()
	err = e.save(task)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.cleanExpired(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(cfg.OutputDir, "images", task.ID)); !os.IsNotExist(err) {
		t.Fatal("expired files not removed")
	}
	again, err := e.Submit("generate", r)
	if err != nil || again.ID != task.ID || again.Status != "expired" {
		t.Fatalf("expired task resubmitted: %+v %v", again, err)
	}
}
