package main

import (
	"context"
	"encoding/json"
	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"testing"
	"time"
)

func TestIntegrationResultSafetyAndCancellationDuringBackoff(t *testing.T) {
	e, ctx := integrationExecutor(t)
	run, err := e.CreateRun(ctx, "retry", []StepTemplate{{Name: "incubate", DeviceID: "incubator-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for ctx.Err() == nil {
		err = e.db.QueryRowContext(ctx, "SELECT command FROM attempts WHERE run_id=$1", run.ID).Scan(&b)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var c lab.Command
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := lab.Report{Version: lab.Version, ID: "test-result-" + c.JobID, Command: c, Revision: 1, Terminal: true, Executions: []lab.Execution{{ID: "exec-1", Outcome: "failed", StartedAt: now, FinishedAt: &now, Retryable: true, Error: "instrument failed", DataState: lab.NotApplicable}}}
	b, _ = json.Marshal(r)
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := e.CancelRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunCancelled || got.Steps[0].Status != StepFailed {
		t.Fatalf("backoff never settled: %+v", got)
	}
}
func TestIntegrationRejectUnprovenSuccess(t *testing.T) {
	e, ctx := integrationExecutor(t)
	run, err := e.CreateRun(ctx, "read", []StepTemplate{{Name: "read", DeviceID: "plate-reader-1"}})
	if err != nil {
		t.Fatal(err)
	}
	e.StartRun(ctx, run.ID)
	var b []byte
	for ctx.Err() == nil {
		err = e.db.QueryRowContext(ctx, "SELECT command FROM attempts WHERE run_id=$1", run.ID).Scan(&b)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var c lab.Command
	json.Unmarshal(b, &c)
	now := time.Now().UTC()
	r := lab.Report{Version: lab.Version, ID: "unproven-" + c.JobID, Command: c, Revision: 1, Terminal: true, Executions: []lab.Execution{{ID: "exec-1", Outcome: "completed", StartedAt: now, FinishedAt: &now, DataState: lab.Stored}}}
	b, _ = json.Marshal(r)
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, _ := e.GetRun(ctx, run.ID)
	if got.Status == RunCompleted {
		t.Fatal("completed a read without a hash or data reference")
	}
}

func TestIntegrationOutboxFailureRollsBackAuthorization(t *testing.T) {
	e, ctx := integrationExecutor(t)
	_, err := e.db.ExecContext(ctx, `CREATE OR REPLACE FUNCTION reject_test_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected outbox failure'; END $$; CREATE TRIGGER reject_test_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_test_outbox()`)
	if err != nil {
		t.Fatal(err)
	}
	defer e.db.ExecContext(context.Background(), "DROP TRIGGER IF EXISTS reject_test_outbox ON outbox; DROP FUNCTION IF EXISTS reject_test_outbox()")
	run, err := e.CreateRun(ctx, "atomic", []StepTemplate{{Name: "atomic", DeviceID: "incubator-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.StartRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.schedule(ctx); err == nil {
		t.Fatal("injected outbox failure was ignored")
	}
	for _, query := range []string{"SELECT count(*) FROM attempts WHERE run_id=$1", "SELECT count(*) FROM outbox WHERE run_id=$1", "SELECT count(*) FROM reservations JOIN attempts USING(job_id) WHERE run_id=$1"} {
		var n int
		if err = e.db.QueryRowContext(ctx, query, run.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partially committed authorization: %d %v", n, err)
		}
	}
}
