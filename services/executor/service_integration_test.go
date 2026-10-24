package main

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func integrationExecutor(t *testing.T) (*Executor, context.Context) {
	t.Helper()
	dsn := os.Getenv("LAB_TEST_DATABASE")
	url := os.Getenv("LAB_TEST_NATS")
	if dsn == "" || url == "" {
		t.Skip("set LAB_TEST_DATABASE and LAB_TEST_NATS for real service integration tests")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	e := NewExecutor(db, nc, nil)
	if err = e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e, ctx
}
func TestIntegrationCancellationAndOutbox(t *testing.T) {
	e, ctx := integrationExecutor(t)
	steps := []StepTemplate{{Name: "fill", DeviceID: "liquid-handler-1"}}
	run, err := e.CreateRun(ctx, "test", steps)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := e.CancelRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != RunCancelled || cancelled.Steps[0].Status != StepCancelled {
		t.Fatalf("cancelled pending run: %+v", cancelled)
	}
	run, err = e.CreateRun(ctx, "test", steps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		var n int
		err = e.db.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE run_id=$1", run.ID).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err = e.StartRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = e.db.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE run_id=$1", run.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate authorization: %d %v", n, err)
	}
	var pending int
	if err = e.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE run_id=$1", run.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("outbox not atomic: %d %v", pending, err)
	}
	cancelled, err = e.CancelRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != RunRunning {
		t.Fatal("cancel hid outstanding command")
	}
}
