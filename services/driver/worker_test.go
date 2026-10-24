package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/automata-tech/distributed-systems-interview/services/driver/instrument"
)

type memoryStore struct {
	objects  map[string][]byte
	failures int
}

func (m *memoryStore) Exists(_ context.Context, k string) (bool, error) {
	_, ok := m.objects[k]
	return ok, nil
}
func (m *memoryStore) Get(_ context.Context, k string) ([]byte, error) { return m.objects[k], nil }
func (m *memoryStore) Put(_ context.Context, k string, b []byte) (string, error) {
	if m.failures > 0 {
		m.failures--
		return "", fmt.Errorf("storage unavailable")
	}
	m.objects[k] = append([]byte{}, b...)
	return "s3://test/" + k, nil
}
func testWorker(t *testing.T, hang bool) (*worker, lab.Command) {
	t.Helper()
	inst, err := instrument.New(instrument.Config{DeviceID: "plate-reader-1", Duration: 10 * time.Millisecond, HangDuration: 40 * time.Millisecond, HangPct: func() int {
		if hang {
			return 100
		}
		return 0
	}(), ProducesData: true, FailuresRetryable: true, LedgerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := lab.OpenSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { spool.Close() })
	now := time.Now().UTC()
	c := lab.Command{Version: lab.Version, ID: "cmd-1", JobID: "job-1", RunID: "run-1", Step: "read", Device: "plate-reader-1", Attempt: 1, CreatedAt: now, DispatchDeadline: now.Add(lab.DispatchTimeout), RecoveryTimeout: lab.DispatchTimeout}
	return &worker{inst: inst, spool: spool, store: &memoryStore{objects: map[string][]byte{}}}, c
}
func TestWorkerRedeliveryAndUploadRecovery(t *testing.T) {
	w, c := testWorker(t, false)
	w.store.(*memoryStore).failures = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	publish := func(r lab.Report) error { return nil }
	r, err := w.run(ctx, c, publish)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Terminal || len(r.Executions) != 1 || r.Executions[0].DataState != lab.Stored {
		t.Fatalf("unproven result: %+v", r)
	}
	x := r.Executions[0]
	for _, data := range w.store.(*memoryStore).objects {
		if fmt.Sprintf("%x", sha256.Sum256(data)) != x.Hash {
			t.Fatal("wrong stored bytes")
		}
	}
	again, err := w.run(ctx, c, publish)
	if err != nil || again.Revision != r.Revision {
		t.Fatalf("replay changed: %v", err)
	}
	if len(w.inst.Status().Executions) != 1 {
		t.Fatal("redelivery repeated physical work")
	}
}
func TestWorkerHungCallSettlesFromLedger(t *testing.T) {
	w, c := testWorker(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := w.run(ctx, c, func(lab.Report) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !r.Terminal || r.Executions[0].Outcome != "hung" || !r.Executions[0].Retryable {
		t.Fatalf("hang never reconciled: %+v", r)
	}
}
func TestWorkerRestartRecoversCapturedBytes(t *testing.T) {
	w, c := testWorker(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := w.run(ctx, c, func(lab.Report) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	original := w.store.(*memoryStore)
	var before []byte
	for _, b := range original.objects {
		before = append([]byte{}, b...)
	}
	r2, err := w.run(ctx, c, func(lab.Report) error { return nil })
	if err != nil || r2.Executions[0].Hash != r.Executions[0].Hash {
		t.Fatal("lost measurement identity")
	}
	for _, b := range original.objects {
		if !bytes.Equal(before, b) {
			t.Fatal("measurement changed")
		}
	}
}

func TestWorkerRecoverBytesAfterAdapterRestart(t *testing.T) {
	w, c := testWorker(t, false)
	store := w.store.(*memoryStore)
	store.failures = 100
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := w.run(ctx, c, func(r lab.Report) error {
		if len(r.Executions) == 1 && r.Executions[0].Outcome == "completed" && r.Executions[0].DataState == lab.Pending {
			cancel()
		}
		return nil
	})
	cancel()
	if err == nil {
		t.Fatal("expected interrupted adapter")
	}
	var persisted durableJob
	if err = w.spool.Load(c.JobID, &persisted); err != nil || len(persisted.Bytes) == 0 {
		t.Fatalf("captured bytes missing: %v", err)
	}
	store.failures = 0
	restarted := worker{inst: w.inst, spool: w.spool, store: store}
	recovered, err := restarted.run(context.Background(), c, func(lab.Report) error { return nil })
	if err != nil || !recovered.Terminal || recovered.Executions[0].DataState != lab.Stored {
		t.Fatalf("spool not recovered: %+v %v", recovered, err)
	}
	if len(w.inst.Status().Executions) != 1 {
		t.Fatal("restart re-read the plate")
	}
}

func TestWorkerLostBytesDoesNotReread(t *testing.T) {
	w, c := testWorker(t, false)
	outcome, err := w.inst.Run(context.Background(), instrument.Job{JobID: c.JobID, RunID: c.RunID, StepName: c.Step})
	if err != nil || len(outcome.Data) == 0 {
		t.Fatalf("instrument failed: %v", err)
	}
	// Simulate process death between the fixed instrument finishing and adapter capture.
	r, err := w.run(context.Background(), c, func(lab.Report) error { return nil })
	if err != nil || !r.Terminal || r.Executions[0].Outcome != "completed" || r.Executions[0].DataState != lab.Lost {
		t.Fatalf("loss hidden: %+v %v", r, err)
	}
	if len(w.inst.Status().Executions) != 1 {
		t.Fatal("data loss repeated measurement")
	}
}
