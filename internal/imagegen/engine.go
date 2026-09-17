package imagegen

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type job struct {
	Operation string
	Request   Request
	Model     Model
}
type Engine struct {
	cfg      Config
	db, lock *sql.DB
	images   *ImageStore
	provider *Provider
	ctx      context.Context
	stop     context.CancelFunc
	mu       sync.Mutex
	active   map[string]context.CancelFunc
	wake     chan struct{}
	wg       sync.WaitGroup
	closed   bool
}

func NewEngine(c Config) (*Engine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.OutputDir, 0700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(c.OutputDir)
	if err != nil {
		return nil, err
	}
	c.OutputDir = abs
	openDB := func(name string) (*sql.DB, error) {
		path := filepath.Join(c.OutputDir, name)
		f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
		db, e := sql.Open("sqlite", path)
		if e != nil {
			return nil, e
		}
		db.SetMaxOpenConns(1)
		return db, nil
	}
	// A separate SQLite database holds an OS-backed exclusive lock for the
	// process lifetime, including across platforms. Crash recovery releases it.
	lock, err := openDB("instance.db")
	if err != nil {
		return nil, err
	}
	if _, err = lock.Exec("BEGIN EXCLUSIVE"); err != nil {
		lock.Close()
		return nil, fmt.Errorf("output directory is in use by another server: %w", err)
	}
	db, err := openDB("tasks.db")
	if err != nil {
		lock.Close()
		return nil, err
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS tasks (
 id TEXT PRIMARY KEY, request_key TEXT UNIQUE, fingerprint TEXT NOT NULL,
 status TEXT NOT NULL, created TEXT NOT NULL, document BLOB NOT NULL, payload BLOB
);
CREATE INDEX IF NOT EXISTS tasks_state ON tasks(status, created);`); err != nil {
		db.Close()
		lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	images := NewImageStore(c)
	e := &Engine{cfg: c, db: db, lock: lock, images: images, provider: NewProvider(images), ctx: ctx, stop: cancel, active: map[string]context.CancelFunc{}, wake: make(chan struct{}, c.Concurrency)}
	if err = e.recover(); err != nil {
		cancel()
		db.Close()
		lock.Close()
		return nil, err
	}
	for i := 0; i < c.Concurrency; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	e.wg.Add(1)
	go e.cleaner()
	return e, nil
}
func (e *Engine) recover() error {
	rows, err := e.db.Query("SELECT document FROM tasks WHERE status IN ('running','preparing')")
	if err != nil {
		return err
	}
	var tasks []Task
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		var t Task
		if err = json.Unmarshal(b, &t); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status == "running" {
			t.Status = "unknown"
			t.Error = unknown("server restarted after submission; generation may have completed; do not retry automatically")
		} else {
			t.Status = "queued"
		}
		if err = e.save(t); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) Submit(operation string, input Request) (Task, error) {
	r, m, err := e.cfg.Normalize(operation, input)
	if err != nil {
		return Task{}, err
	}
	j := job{Operation: operation, Request: cloneJSON(r), Model: cloneJSON(m)}
	payload, err := json.Marshal(j)
	if err != nil {
		return Task{}, invalid("request cannot be encoded")
	}
	if int64(len(payload)) > e.cfg.MaxImageBytes*int64(m.MaxInputImages+1)*4/3+(1<<20) {
		return Task{}, invalid("request too large")
	}
	h := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(h[:])
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Task{}, fail("shutting_down", "server is shutting down")
	}
	if r.RequestID != "" {
		var oldHash string
		var b []byte
		err = e.db.QueryRow("SELECT fingerprint, document FROM tasks WHERE request_key=?", r.RequestID).Scan(&oldHash, &b)
		if err == nil {
			if oldHash != fingerprint {
				return Task{}, fail("idempotency_conflict", "request_id already belongs to different parameters or model configuration")
			}
			var t Task
			err = json.Unmarshal(b, &t)
			return visibleTask(t), err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Task{}, err
		}
	}
	if m.credential() == "" {
		return Task{}, fail("configuration_error", "selected model has no token; configure token or its token_env variable")
	}
	var count int
	if err = e.db.QueryRow("SELECT count(*) FROM tasks WHERE status IN ('queued','preparing','running')").Scan(&count); err != nil {
		return Task{}, err
	}
	if count >= e.cfg.QueueSize+e.cfg.Concurrency {
		return Task{}, fail("queue_full", "task capacity reached; try again later")
	}
	now := time.Now().UTC()
	t := Task{ID: randomID(), Model: r.Model, Operation: operation, Status: "queued", CreatedAt: now, UpdatedAt: now}
	b, _ := json.Marshal(t)
	var key any
	if r.RequestID != "" {
		key = r.RequestID
	}
	_, err = e.db.Exec("INSERT INTO tasks(id, request_key, fingerprint, status, created, document, payload) VALUES(?,?,?,?,?,?,?)", t.ID, key, fingerprint, t.Status, now.Format(time.RFC3339Nano), b, payload)
	if err != nil {
		return Task{}, err
	}
	e.signal()
	return t, nil
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (e *Engine) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
func (e *Engine) get(id string) (Task, error) {
	var b []byte
	err := e.db.QueryRow("SELECT document FROM tasks WHERE id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fail("not_found", "task not found")
	}
	if err != nil {
		return Task{}, err
	}
	var t Task
	err = json.Unmarshal(b, &t)
	return visibleTask(t), err
}

// Hide expired assets immediately; the cleaner removes their files and updates
// the durable record shortly afterwards. Keep the request key for deduplication.
func visibleTask(t Task) Task {
	if t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()) && terminal(t.Status) {
		t.Status = "expired"
		t.Assets = nil
	}
	return t
}
func (e *Engine) Get(id string) (Task, error) { e.mu.Lock(); defer e.mu.Unlock(); return e.get(id) }
func (e *Engine) save(t Task) error {
	t.UpdatedAt = time.Now().UTC()
	if terminal(t.Status) && t.ExpiresAt == nil && duration(e.cfg.Retention) > 0 {
		expires := t.UpdatedAt.Add(duration(e.cfg.Retention))
		t.ExpiresAt = &expires
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if terminal(t.Status) {
		_, err = e.db.Exec("UPDATE tasks SET status=?, document=?, payload=NULL WHERE id=?", t.Status, b, t.ID)
	} else {
		_, err = e.db.Exec("UPDATE tasks SET status=?, document=? WHERE id=?", t.Status, b, t.ID)
	}
	return err
}
func (e *Engine) Wait(ctx context.Context, id string, timeout time.Duration) (Task, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, err := e.Get(id)
		if err != nil || terminal(t.Status) {
			return t, err
		}
		select {
		case <-ctx.Done():
			return e.Get(id)
		case <-ticker.C:
		}
	}
}
func (e *Engine) Cancel(id string) (Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, err := e.get(id)
	if err != nil || terminal(t.Status) {
		return t, err
	}
	if cancel, ok := e.active[id]; ok {
		cancel()
	}
	if t.Status == "running" {
		t.Status = "unknown"
		t.Error = unknown("local task cancelled after submission; upstream may still finish and bill")
	} else {
		t.Status = "cancelled"
		t.Error = fail("cancelled", "cancelled before upstream submission")
	}
	if err = e.save(t); err != nil {
		return Task{}, err
	}
	return e.get(id)
}
func (e *Engine) claim() (Task, job, context.Context, context.CancelFunc, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var t Task
	var j job
	if e.closed {
		return t, j, nil, nil, sql.ErrNoRows
	}
	var doc, payload []byte
	err := e.db.QueryRow("SELECT document,payload FROM tasks WHERE status='queued' ORDER BY created LIMIT 1").Scan(&doc, &payload)
	if err != nil {
		return t, j, nil, nil, err
	}
	if err = json.Unmarshal(doc, &t); err != nil {
		return t, j, nil, nil, err
	}
	if err = json.Unmarshal(payload, &j); err != nil {
		return t, j, nil, nil, err
	}
	t.Status = "preparing"
	if err = e.save(t); err != nil {
		return t, j, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, duration(e.cfg.TaskTimeout))
	e.active[t.ID] = cancel
	return t, j, ctx, cancel, nil
}
func (e *Engine) worker() {
	defer e.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if e.ctx.Err() != nil {
			return
		}
		t, j, ctx, cancel, err := e.claim()
		if err == nil {
			e.process(ctx, t, j)
			cancel()
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("task claim failed", "error", err)
		}
		select {
		case <-e.ctx.Done():
			return
		case <-e.wake:
		case <-ticker.C:
		}
	}
}
func (e *Engine) process(ctx context.Context, t Task, j job) {
	var inputs []Picture
	var mask *Picture
	var workErr error
	for _, in := range j.Request.Images {
		var pic Picture
		pic, workErr = e.images.Resolve(ctx, in)
		if workErr != nil {
			break
		}
		inputs = append(inputs, pic)
	}
	if workErr == nil && j.Request.Mask != nil {
		var pic Picture
		pic, workErr = e.images.Resolve(ctx, *j.Request.Mask)
		if workErr == nil {
			mask = &pic
			workErr = validateMask(inputs[0], pic)
		}
	}
	if workErr == nil {
		workErr = ctx.Err()
	}
	if workErr == nil {
		// Attach a credential only to the in-memory request, never to the
		// serialized job or its idempotency fingerprint.
		j.Model.Token, workErr = e.cfg.taskCredential(j.Request.Model, j.Model)
	}
	if workErr == nil {
		e.mu.Lock()
		current, err := e.get(t.ID)
		if err == nil && !terminal(current.Status) && ctx.Err() == nil {
			t.Status = "running"
			err = e.save(t)
		} else if err == nil {
			err = context.Canceled
		}
		e.mu.Unlock()
		workErr = err
	}
	var assets []Asset
	if workErr == nil {
		var pics []Picture
		pics, workErr = e.provider.Generate(ctx, j.Model, j.Request, inputs, mask)
		if workErr == nil {
			assets, workErr = e.images.Save(t.ID, pics)
			if workErr != nil {
				workErr = unknown("generation completed but saving output failed; do not regenerate automatically")
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, t.ID)
	current, err := e.get(t.ID)
	if err != nil {
		slog.Error("cannot load finishing task", "task_id", t.ID)
		return
	}
	if terminal(current.Status) {
		return
	}
	if e.closed && current.Status == "preparing" {
		current.Status = "queued"
		current.Error = nil
		if err = e.save(current); err != nil {
			slog.Error("cannot preserve unsubmitted task", "task_id", t.ID, "error", err)
		}
		return
	}
	if workErr == nil {
		current.Status = "succeeded"
		current.Assets = assets
	} else {
		var te *TaskError
		if !errors.As(workErr, &te) {
			if current.Status == "running" {
				te = unknown("task interrupted after submission; upstream may still finish and bill")
			} else {
				te = fail("input_error", "could not prepare input images before timeout or shutdown")
			}
		}
		current.Error = te
		current.Status = "failed"
		if te.ResultUnknown {
			current.Status = "unknown"
		}
	}
	if err = e.save(current); err != nil {
		slog.Error("cannot persist task result", "task_id", t.ID, "error", err)
	}
}
func (e *Engine) cleanExpired() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.db.Query("SELECT document FROM tasks WHERE status IN ('succeeded','failed','cancelled','unknown')")
	if err != nil {
		return err
	}
	var expired []Task
	now := time.Now()
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		var t Task
		if err = json.Unmarshal(b, &t); err != nil {
			rows.Close()
			return err
		}
		if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
			expired = append(expired, t)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range expired {
		if _, active := e.active[t.ID]; active {
			continue
		}
		if err = os.RemoveAll(filepath.Join(e.cfg.OutputDir, "images", t.ID)); err != nil {
			return err
		}
		t.Status = "expired"
		t.Assets = nil
		if err = e.save(t); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) cleaner() {
	defer e.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := e.cleanExpired(); err != nil {
			slog.Error("image cleanup failed", "error", err)
		}
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.stop()
	e.mu.Unlock()
	e.wg.Wait()
	return errors.Join(e.db.Close(), e.lock.Close())
}
