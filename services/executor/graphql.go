package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/graphql-go/graphql"
	"github.com/lib/pq"
)

func enum(name string, values ...string) *graphql.Enum {
	config := graphql.EnumValueConfigMap{}
	for _, v := range values {
		config[strings.ToUpper(v)] = &graphql.EnumValueConfig{Value: v}
	}
	return graphql.NewEnum(graphql.EnumConfig{Name: name, Values: config})
}
func fields(types map[string]graphql.Output) graphql.Fields {
	f := graphql.Fields{}
	for n, t := range types {
		f[n] = &graphql.Field{Type: t}
	}
	return f
}
func nonNull(t graphql.Type) *graphql.NonNull { return graphql.NewNonNull(t) }
func list(t graphql.Type) *graphql.NonNull    { return nonNull(graphql.NewList(nonNull(t))) }
func (a *API) graphHandler() http.HandlerFunc {
	schema, err := a.graphSchema()
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
			return
		}
		var body struct {
			Query         string         `json:"query"`
			Variables     map[string]any `json:"variables"`
			OperationName string         `json:"operationName"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&body); err != nil || body.Query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid GraphQL request"})
			return
		}
		result := graphql.Do(graphql.Params{Schema: schema, RequestString: body.Query, VariableValues: body.Variables, OperationName: body.OperationName, Context: r.Context()})
		writeJSON(w, http.StatusOK, result)
	}
}
func (a *API) graphSchema() (graphql.Schema, error) {
	runStatus := enum("RunStatus", "pending", "running", "completed", "failed", "cancelled")
	stepStatus := enum("StepStatus", "pending", "running", "completed", "failed", "cancelled")
	outcome := enum("PhysicalOutcome", "running", "completed", "failed", "hung")
	data := enum("DataState", lab.NotApplicable, lab.Pending, lab.Stored, lab.Lost)
	measurement := graphql.NewObject(graphql.ObjectConfig{Name: "Measurement", Fields: fields(map[string]graphql.Output{"state": nonNull(data), "sha256": graphql.String, "dataRef": graphql.String, "error": graphql.String})})
	execution := graphql.NewObject(graphql.ObjectConfig{Name: "Execution", Fields: fields(map[string]graphql.Output{"id": nonNull(graphql.ID), "outcome": nonNull(outcome), "startedAt": nonNull(graphql.String), "finishedAt": graphql.String, "error": graphql.String, "retryable": graphql.Boolean, "measurement": measurement})})
	attempt := graphql.NewObject(graphql.ObjectConfig{Name: "Attempt", Fields: fields(map[string]graphql.Output{"jobId": nonNull(graphql.ID), "number": nonNull(graphql.Int), "authorizedAt": nonNull(graphql.String), "dispatchDeadline": nonNull(graphql.String), "recoveryDeadline": graphql.String, "recoveryError": graphql.String, "executions": list(execution)})})
	step := graphql.NewObject(graphql.ObjectConfig{Name: "Step", Fields: fields(map[string]graphql.Output{"name": nonNull(graphql.String), "deviceId": nonNull(graphql.ID), "dependsOn": list(graphql.String), "status": nonNull(stepStatus), "startedAt": graphql.String, "finishedAt": graphql.String, "error": graphql.String, "dataRef": graphql.String, "attempts": list(attempt)})})
	run := graphql.NewObject(graphql.ObjectConfig{Name: "Run", Fields: fields(map[string]graphql.Output{"id": nonNull(graphql.ID), "workflowName": nonNull(graphql.String), "status": nonNull(runStatus), "createdAt": nonNull(graphql.String), "startedAt": graphql.String, "finishedAt": graphql.String, "cancellationRequested": nonNull(graphql.Boolean), "dispatchPaused": nonNull(graphql.Boolean), "steps": list(step)})})
	workflowStep := graphql.NewObject(graphql.ObjectConfig{Name: "WorkflowStep", Fields: fields(map[string]graphql.Output{"name": nonNull(graphql.String), "deviceId": nonNull(graphql.ID), "dependsOn": list(graphql.String)})})
	workflow := graphql.NewObject(graphql.ObjectConfig{Name: "Workflow", Fields: fields(map[string]graphql.Output{"name": nonNull(graphql.String), "isDefault": nonNull(graphql.Boolean), "steps": list(workflowStep)})})
	ids := graphql.FieldConfigArgument{"id": &graphql.ArgumentConfig{Type: nonNull(graphql.ID)}}
	query := graphql.NewObject(graphql.ObjectConfig{Name: "Query", Fields: graphql.Fields{
		"run": &graphql.Field{Type: run, Args: ids, Resolve: func(p graphql.ResolveParams) (any, error) {
			records, err := a.executor.graphRecords(p.Context, p.Args["id"].(string))
			if errors.Is(err, ErrRunNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, graphError(err)
			}
			return records[0], nil
		}},
		"runs": &graphql.Field{Type: list(run), Resolve: func(p graphql.ResolveParams) (any, error) {
			records, err := a.executor.graphRecords(p.Context, "")
			return records, graphError(err)
		}},
		"workflows": &graphql.Field{Type: list(workflow), Resolve: func(p graphql.ResolveParams) (any, error) {
			set, err := LoadWorkflows(a.workflowsPath)
			if err != nil {
				return nil, graphError(err)
			}
			out := []map[string]any{}
			for _, name := range set.Names() {
				templates, _ := set.Get(name)
				steps := []map[string]any{}
				for _, s := range templates {
					steps = append(steps, map[string]any{"name": s.Name, "deviceId": s.DeviceID, "dependsOn": append([]string{}, s.DependsOn...)})
				}
				out = append(out, map[string]any{"name": name, "isDefault": set.Default() == name, "steps": steps})
			}
			return out, nil
		}},
	}})
	mutation := graphql.NewObject(graphql.ObjectConfig{Name: "Mutation", Fields: graphql.Fields{
		"createRun": &graphql.Field{Type: run, Args: graphql.FieldConfigArgument{"workflowName": &graphql.ArgumentConfig{Type: graphql.String}}, Resolve: func(p graphql.ResolveParams) (any, error) {
			name, _ := p.Args["workflowName"].(string)
			view, err := a.createWorkflowRun(p.Context, name)
			if err != nil {
				return nil, graphError(err)
			}
			rows, err := a.executor.graphRecords(p.Context, view.ID)
			if err != nil {
				return nil, graphError(err)
			}
			return rows[0], nil
		}},
		"startRun":  &graphql.Field{Type: run, Args: ids, Resolve: a.graphMutation(false)},
		"cancelRun": &graphql.Field{Type: run, Args: ids, Resolve: a.graphMutation(true)},
	}})
	return graphql.NewSchema(graphql.SchemaConfig{Query: query, Mutation: mutation})
}
func (a *API) graphMutation(cancel bool) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (any, error) {
		id := p.Args["id"].(string)
		var err error
		if cancel {
			_, err = a.executor.CancelRun(p.Context, id)
		} else {
			_, err = a.executor.StartRun(p.Context, id)
		}
		if err != nil {
			return nil, graphError(err)
		}
		rows, err := a.executor.graphRecords(p.Context, id)
		if err != nil {
			return nil, graphError(err)
		}
		return rows[0], nil
	}
}

type invalidWorkflow struct{ error }
type workflowConfigError struct{ error }

func graphError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRunNotFound) {
		return ErrRunNotFound
	}
	var invalid invalidWorkflow
	if errors.As(err, &invalid) {
		return invalid
	}
	log.Printf("graphql: %v", err)
	return errors.New("operation failed")
}
func (a *API) createWorkflowRun(ctx context.Context, name string) (RunView, error) {
	set, err := LoadWorkflows(a.workflowsPath)
	if err != nil {
		return RunView{}, workflowConfigError{err}
	}
	if name == "" {
		name = set.Default()
	}
	steps, err := set.Get(name)
	if err != nil {
		return RunView{}, invalidWorkflow{err}
	}
	return a.executor.CreateRun(ctx, name, steps)
}
func textTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func text(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (e *Executor) graphRecords(ctx context.Context, id string) ([]map[string]any, error) {
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := "SELECT record FROM runs ORDER BY created_at DESC,id"
	args := []any{}
	if id != "" {
		query = "SELECT record FROM runs WHERE id=$1"
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var records []runRecord
	ids := []string{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		var rec runRecord
		if err = json.Unmarshal(b, &rec); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, rec)
		ids = append(ids, rec.View.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if id != "" && len(records) == 0 {
		return nil, ErrRunNotFound
	}
	attempts := map[string][]map[string]any{}
	rows, err = tx.QueryContext(ctx, "SELECT command,report FROM attempts WHERE run_id=ANY($1) ORDER BY ordinal,job_id", pq.Array(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cb, rb []byte
		if err = rows.Scan(&cb, &rb); err != nil {
			rows.Close()
			return nil, err
		}
		var c lab.Command
		var rep lab.Report
		if err = json.Unmarshal(cb, &c); err != nil {
			rows.Close()
			return nil, err
		}
		if len(rb) > 0 {
			if err = json.Unmarshal(rb, &rep); err != nil {
				rows.Close()
				return nil, err
			}
		}
		executions := []map[string]any{}
		for _, x := range rep.Executions {
			var measure any
			if c.Device == "plate-reader-1" {
				measure = map[string]any{"state": x.DataState, "sha256": text(x.Hash), "dataRef": text(x.DataRef), "error": text(x.DataError)}
			}
			var retry any
			if x.Outcome == "failed" || x.Outcome == "hung" {
				retry = x.Retryable
			}
			executions = append(executions, map[string]any{"id": x.ID, "outcome": x.Outcome, "startedAt": textTime(&x.StartedAt), "finishedAt": textTime(x.FinishedAt), "error": text(x.Error), "retryable": retry, "measurement": measure})
		}
		key := c.RunID + "/" + c.Step
		attempts[key] = append(attempts[key], map[string]any{"jobId": c.JobID, "number": c.Attempt, "authorizedAt": textTime(&c.CreatedAt), "dispatchDeadline": textTime(&c.DispatchDeadline), "recoveryDeadline": textTime(rep.RecoveryDeadline), "recoveryError": text(rep.Error), "executions": executions})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range records {
		steps := []map[string]any{}
		for _, s := range r.View.Steps {
			history := attempts[r.View.ID+"/"+s.Name]
			if history == nil {
				history = []map[string]any{}
			}
			if r.Pause != "" && r.State[s.Name].Job != "" && len(history) > 0 {
				history[len(history)-1]["recoveryError"] = text(r.Pause)
			}
			steps = append(steps, map[string]any{"name": s.Name, "deviceId": s.DeviceID, "dependsOn": s.DependsOn, "status": s.Status, "startedAt": textTime(s.StartedAt), "finishedAt": textTime(s.FinishedAt), "error": s.Error, "dataRef": s.DataRef, "attempts": history})
		}
		out = append(out, map[string]any{"id": r.View.ID, "workflowName": r.View.WorkflowName, "status": r.View.Status, "createdAt": textTime(&r.View.CreatedAt), "startedAt": textTime(r.View.StartedAt), "finishedAt": textTime(r.View.FinishedAt), "cancellationRequested": r.Cancel, "dispatchPaused": r.Pause != "", "steps": steps})
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	return out, nil
}
