package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/automata-tech/distributed-systems-interview/services/common/storage"
	"github.com/nats-io/nats.go"
)

var ErrRunNotFound = errors.New("run not found")
var ErrNotImplemented = errors.New("not implemented")

const stateLock int64 = 472015

type stepRuntime struct {
	Attempts int
	Job      string
	RetryAt  *time.Time
}
type runRecord struct {
	View   RunView
	Cancel bool
	Pause  string
	State  map[string]*stepRuntime
}
type Executor struct {
	db      *sql.DB
	nc      *nats.Conn
	storage *storage.Storage
	js      nats.JetStreamContext
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	once    sync.Once
}

func NewExecutor(db *sql.DB, nc *nats.Conn, st *storage.Storage) *Executor {
	return &Executor{db: db, nc: nc, storage: st}
}
func (e *Executor) Start(ctx context.Context) error {
	var version int
	if err := e.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 1 {
		return fmt.Errorf("incompatible database; recreate only disposable database: %v", err)
	}
	js, err := lab.Provision(e.nc)
	if err != nil {
		return err
	}
	e.js = js
	owned, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.wg.Add(3)
	go func() { defer e.wg.Done(); e.scheduleLoop(owned) }()
	go func() { defer e.wg.Done(); e.publishLoop(owned) }()
	go func() { defer e.wg.Done(); e.resultLoop(owned) }()
	return nil
}
func (e *Executor) Close() {
	e.once.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}
		e.wg.Wait()
	})
}
func (e *Executor) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", stateLock); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func loadRecord(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (runRecord, error) {
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT record FROM runs WHERE id=$1", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return runRecord{}, ErrRunNotFound
	}
	if err != nil {
		return runRecord{}, err
	}
	var r runRecord
	err = json.Unmarshal(b, &r)
	return r, err
}
func saveRecord(ctx context.Context, tx *sql.Tx, r runRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE runs SET record=$1 WHERE id=$2", b, r.View.ID)
	return err
}
func (e *Executor) CreateRun(ctx context.Context, name string, steps []StepTemplate) (RunView, error) {
	for _, s := range steps {
		if !lab.SafeID(s.Name) || !lab.Supported(s.DeviceID) {
			return RunView{}, fmt.Errorf("unsupported step/device %s/%s", s.Name, s.DeviceID)
		}
	}
	id := "run-" + randomHex(12)
	r := runRecord{View: RunView{ID: id, WorkflowName: name, Status: RunPending, CreatedAt: time.Now().UTC(), Steps: []StepView{}}, State: map[string]*stepRuntime{}}
	for _, s := range steps {
		r.View.Steps = append(r.View.Steps, StepView{Name: s.Name, DeviceID: s.DeviceID, DependsOn: append([]string{}, s.DependsOn...), Status: StepPending})
		r.State[s.Name] = &stepRuntime{}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return RunView{}, err
	}
	_, err = e.db.ExecContext(ctx, "INSERT INTO runs(id,created_at,record) VALUES($1,$2,$3)", id, r.View.CreatedAt, b)
	return r.View, err
}
func (e *Executor) StartRun(ctx context.Context, id string) (RunView, error) {
	var view RunView
	err := e.transaction(ctx, func(tx *sql.Tx) error {
		r, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.View.Status == RunPending {
			now := time.Now().UTC()
			r.View.Status = RunRunning
			r.View.StartedAt = &now
		}
		view = r.View
		return saveRecord(ctx, tx, r)
	})
	return view, err
}
func (e *Executor) CancelRun(ctx context.Context, id string) (RunView, error) {
	var view RunView
	err := e.transaction(ctx, func(tx *sql.Tx) error {
		r, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.View.Status == RunPending || r.View.Status == RunRunning {
			r.Cancel = true
			settle(&r)
		}
		view = r.View
		return saveRecord(ctx, tx, r)
	})
	return view, err
}
func (e *Executor) GetRun(ctx context.Context, id string) (RunView, error) {
	r, err := loadRecord(ctx, e.db, id)
	return r.View, err
}
func (e *Executor) ListRuns(ctx context.Context) ([]RunView, error) {
	rows, err := e.db.QueryContext(ctx, "SELECT record FROM runs ORDER BY created_at DESC,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunView{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var r runRecord
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r.View)
	}
	return out, rows.Err()
}
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func settle(r *runRecord) {
	failed := false
	for _, s := range r.View.Steps {
		if s.Status == StepFailed {
			failed = true
		}
	}
	if r.Cancel || failed {
		for i := range r.View.Steps {
			s := &r.View.Steps[i]
			state := r.State[s.Name]
			if state.Job != "" {
				continue
			}
			if state.RetryAt != nil {
				state.RetryAt = nil
				s.Status = StepFailed
				s.Error = ptr("retry suppressed by run stop")
			} else if s.Status == StepPending {
				s.Status = StepCancelled
				reason := "not started: another step failed"
				if r.Cancel {
					reason = "not started: run cancelled"
				}
				s.Error = ptr(reason)
			}
		}
	}
	outstanding := false
	all := true
	for _, s := range r.View.Steps {
		if s.Status == StepRunning || s.Status == StepPending {
			outstanding = true
		}
		if s.Status != StepCompleted {
			all = false
		}
	}
	if outstanding {
		return
	}
	r.Pause = ""
	now := time.Now().UTC()
	r.View.FinishedAt = &now
	switch {
	case r.Cancel:
		r.View.Status = RunCancelled
	case all:
		r.View.Status = RunCompleted
	default:
		r.View.Status = RunFailed
	}
}
