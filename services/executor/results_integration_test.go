package main

import (
	"context"
	"encoding/json"
	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"strings"
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

func TestIntegrationLateEvidenceClearsUncertainty(t *testing.T) {
	e, ctx := integrationExecutor(t)
	run, err := e.CreateRun(ctx, "late", []StepTemplate{{Name: "late", DeviceID: "incubator-1"}})
	if err != nil {
		t.Fatal(err)
	}
	e.StartRun(ctx, run.ID)
	if err = e.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	var b []byte
	if err = e.db.QueryRowContext(ctx, "SELECT command FROM attempts WHERE run_id=$1", run.ID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	var c lab.Command
	json.Unmarshal(b, &c)
	c.CreatedAt = time.Now().UTC().Add(-time.Minute)
	c.DispatchDeadline = c.CreatedAt.Add(lab.DispatchTimeout)
	b, _ = json.Marshal(c)
	if _, err = e.db.ExecContext(ctx, "UPDATE attempts SET command=$1 WHERE job_id=$2", b, c.JobID); err != nil {
		t.Fatal(err)
	}
	if err = e.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	paused, err := loadRecord(ctx, e.db, run.ID)
	if err != nil || paused.Pause == "" || paused.View.Status != RunRunning {
		t.Fatalf("timeout fabricated failure: %+v %v", paused, err)
	}
	now := time.Now().UTC()
	r := lab.Report{Version: lab.Version, ID: "late-" + c.JobID, Command: c, Revision: 1, Terminal: true, Executions: []lab.Execution{{ID: "late-execution", Outcome: "completed", StartedAt: now, FinishedAt: &now, DataState: lab.NotApplicable}}}
	b, _ = json.Marshal(r)
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	settled, err := loadRecord(ctx, e.db, run.ID)
	if err != nil || settled.View.Status != RunCompleted || settled.Pause != "" || settled.View.Steps[0].Error != nil {
		t.Fatalf("late proof did not clear uncertainty: %+v %v", settled, err)
	}
}

func TestIntegrationDataLossRetainsPhysicalSuccess(t *testing.T) {
	e, ctx := integrationExecutor(t)
	run, err := e.CreateRun(ctx, "lost", []StepTemplate{{Name: "read", DeviceID: "plate-reader-1"}})
	if err != nil {
		t.Fatal(err)
	}
	e.StartRun(ctx, run.ID)
	if err = e.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	var b []byte
	if err = e.db.QueryRowContext(ctx, "SELECT command FROM attempts WHERE run_id=$1", run.ID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	var c lab.Command
	json.Unmarshal(b, &c)
	now := time.Now().UTC()
	r := lab.Report{Version: lab.Version, ID: "lost-" + c.JobID, Command: c, Revision: 1, Terminal: true, Executions: []lab.Execution{{ID: "lost-execution", Outcome: "completed", StartedAt: now, FinishedAt: &now, Hash: strings.Repeat("a", 64), DataState: lab.Lost, DataError: "measurement_data_lost"}}}
	b, _ = json.Marshal(r)
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	view, err := e.GetRun(ctx, run.ID)
	if err != nil || view.Status != RunFailed || view.Steps[0].Error == nil || *view.Steps[0].Error != "measurement_data_lost" {
		t.Fatalf("data loss hidden: %+v %v", view, err)
	}
	var stored []byte
	if err = e.db.QueryRowContext(ctx, "SELECT report FROM attempts WHERE run_id=$1", run.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var evidence lab.Report
	json.Unmarshal(stored, &evidence)
	if evidence.Executions[0].Outcome != "completed" {
		t.Fatal("physical success rewritten as failure")
	}
	response := graphRequest(&API{executor: e, workflowsPath: "../../config/workflows.yaml"}, `{run(id:"`+run.ID+`"){status steps{status attempts{executions{outcome measurement{state sha256}}}}}}`)
	if strings.Contains(response.Body.String(), `"errors"`) || !strings.Contains(response.Body.String(), `"state":"LOST"`) || !strings.Contains(response.Body.String(), `"outcome":"COMPLETED"`) {
		t.Fatalf("scientific data loss is not visible through GraphQL: %s", response.Body.String())
	}
}

func TestIntegrationResultConsumerRetriesBindFailureUntilCancelled(t *testing.T) {
	e, parent := integrationExecutor(t)
	e.nc.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go func() { e.resultLoop(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("result consumer silently exited on initial binding failure")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("result consumer ignored cancellation")
	}
}

func TestIntegrationDataVerificationGatesDependentDispatch(t *testing.T) {
	e, ctx := integrationExecutor(t)
	templates := []StepTemplate{{Name: "read", DeviceID: "plate-reader-1"}, {Name: "incubate", DeviceID: "incubator-1", DependsOn: []string{"read"}}}
	run, err := e.CreateRun(ctx, "data-gate", templates)
	if err != nil {
		t.Fatal(err)
	}
	e.StartRun(ctx, run.ID)
	if err = e.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	var b []byte
	if err = e.db.QueryRowContext(ctx, "SELECT command FROM attempts WHERE run_id=$1", run.ID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	var c lab.Command
	json.Unmarshal(b, &c)
	now := time.Now().UTC()
	pending := lab.Report{Version: lab.Version, ID: "pending-" + c.JobID, Command: c, Revision: 1, Executions: []lab.Execution{{ID: "read-proof", Outcome: "completed", StartedAt: now, FinishedAt: &now, Hash: strings.Repeat("b", 64), DataState: lab.Pending}}}
	b, _ = json.Marshal(pending)
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err = e.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = e.db.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE run_id=$1", run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dependent authorized before data verification: %d %v", count, err)
	}
	verified := pending
	verified.ID = "verified-" + c.JobID
	verified.Revision = 2
	verified.Terminal = true
	verified.Executions = append([]lab.Execution{}, pending.Executions...)
	verified.Executions[0].DataState = lab.Stored
	verified.Executions[0].DataRef = "s3://test-data/runs/" + run.ID + "/jobs/" + c.JobID + "/executions/read-proof/measurement.csv"
	proofTime := time.Now().UTC()
	b, _ = json.Marshal(verified)
	if err = e.applyReport(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err = e.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRowContext(ctx, "SELECT command FROM attempts WHERE run_id=$1 AND step_name='incubate'", run.ID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	var child lab.Command
	json.Unmarshal(b, &child)
	if child.CreatedAt.Before(proofTime) {
		t.Fatal("persisted dispatch predates verified prerequisite evidence")
	}
}
