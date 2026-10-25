package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/nats-io/nats.go"
)

func (e *Executor) scheduleLoop(ctx context.Context) {
	for ctx.Err() == nil {
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := e.schedule(call)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("schedule: %v", err)
		}
		if !lab.Wait(ctx, lab.PollInterval) {
			return
		}
	}
}
func (e *Executor) schedule(ctx context.Context) error {
	return e.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM runs ORDER BY created_at,id")
		if err != nil {
			return err
		}
		var records []runRecord
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return err
			}
			var r runRecord
			if err = json.Unmarshal(b, &r); err != nil {
				rows.Close()
				return err
			}
			records = append(records, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.View.Status != RunRunning {
				continue
			}
			if r.Pause != "report evidence conflict" {
				r.Pause = ""
			}
			now := time.Now().UTC()
			for i := range r.View.Steps {
				s := &r.View.Steps[i]
				state := r.State[s.Name]
				if state.Job == "" {
					continue
				}
				var cmdBytes, repBytes []byte
				if err = tx.QueryRowContext(ctx, "SELECT command,report FROM attempts WHERE job_id=$1", state.Job).Scan(&cmdBytes, &repBytes); err != nil {
					return err
				}
				var c lab.Command
				json.Unmarshal(cmdBytes, &c)
				var rep lab.Report
				if len(repBytes) > 0 {
					json.Unmarshal(repBytes, &rep)
				}
				deadline := c.DispatchDeadline
				if rep.RecoveryDeadline != nil {
					deadline = *rep.RecoveryDeadline
				}
				if (len(rep.Executions) == 0 || !rep.Terminal) && now.After(deadline) {
					s.Error = ptr("recovery unresolved: deadline exceeded")
					r.Pause = "recovery unresolved"
				}
				if rep.Error != "" {
					s.Error = ptr(rep.Error)
					r.Pause = rep.Error
				}
			}
			settle(&r)
			if r.View.Status != RunRunning {
				if err = saveRecord(ctx, tx, r); err != nil {
					return err
				}
				continue
			}
			stopped := r.Cancel || r.Pause != ""
			for _, s := range r.View.Steps {
				if s.Status == StepFailed {
					stopped = true
				}
			}
			if !stopped {
				projection := []StepState{}
				for _, s := range r.View.Steps {
					status := s.Status
					st := r.State[s.Name]
					if st.RetryAt != nil && st.Job == "" {
						if !now.Before(*st.RetryAt) {
							status = StepPending
						} else {
							status = "backoff"
						}
					}
					projection = append(projection, StepState{Name: s.Name, DeviceID: s.DeviceID, DependsOn: s.DependsOn, Status: status})
				}
				for _, name := range NextSteps(projection) {
					for i := range r.View.Steps {
						s := &r.View.Steps[i]
						if s.Name != name {
							continue
						}
						st := r.State[name]
						job := "job-" + randomHex(12)
						c := lab.Command{Version: lab.Version, ID: "cmd-" + job, RunID: r.View.ID, Step: name, Device: s.DeviceID, JobID: job, Attempt: st.Attempts + 1, CreatedAt: now, DispatchDeadline: now.Add(lab.DispatchTimeout), RecoveryTimeout: recoveryTimeout()}
						b, _ := json.Marshal(c)
						var reserved int
						if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM reservations WHERE device_id=$1", s.DeviceID).Scan(&reserved); err != nil {
							return err
						}
						if reserved > 0 {
							continue
						}
						if _, err = tx.ExecContext(ctx, "INSERT INTO attempts(job_id,run_id,step_name,ordinal,command) VALUES($1,$2,$3,$4,$5)", job, r.View.ID, name, c.Attempt, b); err != nil {
							return err
						}
						if _, err = tx.ExecContext(ctx, "INSERT INTO reservations(device_id,job_id) VALUES($1,$2)", s.DeviceID, job); err != nil {
							return err
						}
						if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,run_id,command) VALUES($1,$2,$3)", c.ID, r.View.ID, b); err != nil {
							return err
						}
						st.Job = job
						st.Attempts = c.Attempt
						st.RetryAt = nil
						s.Status = StepRunning
					}
				}
			}
			if err = saveRecord(ctx, tx, r); err != nil {
				return err
			}
		}
		return nil
	})
}
func (e *Executor) publishLoop(ctx context.Context) {
	for ctx.Err() == nil {
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := e.publishPending(call)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("outbox: %v", err)
		}
		if !lab.Wait(ctx, lab.PollInterval) {
			return
		}
	}
}
func (e *Executor) publishPending(ctx context.Context) error {
	rows, err := e.db.QueryContext(ctx, "SELECT id,command FROM outbox WHERE NOT published ORDER BY id")
	if err != nil {
		return err
	}
	type item struct {
		id string
		b  []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.b); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		var c lab.Command
		if err = json.Unmarshal(it.b, &c); err != nil {
			return err
		}
		if err = lab.Publish(ctx, e.js, lab.CommandPrefix+c.Device, c.ID, it.b); err != nil {
			return err
		}
		if _, err = e.db.ExecContext(ctx, "UPDATE outbox SET published=true WHERE id=$1", it.id); err != nil {
			return err
		}
	}
	return nil
}
func (e *Executor) resultLoop(ctx context.Context) {
	var sub *nats.Subscription
	var err error
	for ctx.Err() == nil {
		sub, err = e.js.PullSubscribe(lab.ResultPrefix+"*", lab.ExecutorConsumer, nats.Bind(lab.ResultStream, lab.ExecutorConsumer))
		if err == nil {
			break
		}
		log.Printf("result bind: %v", err)
		if !lab.Wait(ctx, time.Second) {
			return
		}
	}
	if sub == nil {
		return
	}
	defer sub.Unsubscribe()
	for ctx.Err() == nil {
		msgs, err := sub.Fetch(32, nats.MaxWait(time.Second))
		if err != nil && !errors.Is(err, nats.ErrTimeout) {
			log.Printf("result fetch: %v", err)
			lab.Wait(ctx, time.Second)
			continue
		}
		for _, m := range msgs {
			call, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = e.applyReport(call, m.Data)
			cancel()
			if err == nil {
				m.Ack()
			} else {
				log.Printf("result apply: %v", err)
			}
		}
	}
}
func (e *Executor) applyReport(ctx context.Context, b []byte) error {
	return e.transaction(ctx, func(tx *sql.Tx) error {
		var rep lab.Report
		err := json.Unmarshal(b, &rep)
		if err != nil || rep.Command.Validate() != nil || rep.Version != lab.Version || rep.Revision < 1 || !lab.SafeID(rep.ID) {
			return reject(ctx, tx, b, "invalid report")
		}
		var command, previous []byte
		if err = tx.QueryRowContext(ctx, "SELECT command,report FROM attempts WHERE job_id=$1", rep.Command.JobID).Scan(&command, &previous); errors.Is(err, sql.ErrNoRows) {
			return reject(ctx, tx, b, "unknown job")
		} else if err != nil {
			return err
		}
		var c lab.Command
		if err = json.Unmarshal(command, &c); err != nil {
			return err
		}
		var old lab.Report
		if len(previous) > 0 {
			if err = json.Unmarshal(previous, &old); err != nil {
				return err
			}
		}
		var seen []byte
		err = tx.QueryRowContext(ctx, "SELECT payload FROM inbox WHERE id=$1", rep.ID).Scan(&seen)
		if err == nil {
			if string(seen) == string(b) {
				return nil
			}
			return reject(ctx, tx, b, "report id conflict")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r, err := loadRecord(ctx, tx, c.RunID)
		if err != nil {
			return err
		}
		conflict := false
		if c != rep.Command {
			conflict = true
		}
		if rep.Revision <= old.Revision {
			return reject(ctx, tx, b, "stale report")
		}
		if lab.CheckReport(old, rep) != nil {
			conflict = true
		}
		if conflict {
			r.Pause = "report evidence conflict"
			for i := range r.View.Steps {
				if r.View.Steps[i].Name == c.Step {
					r.View.Steps[i].Error = ptr(r.Pause)
				}
			}
			if err = saveRecord(ctx, tx, r); err != nil {
				return err
			}
			return reject(ctx, tx, b, r.Pause)
		}
		if rep.Terminal {
			if len(rep.Executions) != 1 {
				return reject(ctx, tx, b, "terminal result needs one execution")
			}
			x := rep.Executions[0]
			if x.Outcome != lab.OutcomeCompleted && x.Outcome != lab.OutcomeFailed && x.Outcome != lab.OutcomeHung {
				return reject(ctx, tx, b, "unknown physical outcome")
			}
			if x.Outcome == lab.OutcomeCompleted && c.Device == lab.PlateReaderID && x.DataState == lab.Stored && (len(x.Hash) != 64 || !strings.HasPrefix(x.DataRef, "s3://") || !strings.Contains(x.DataRef, "/executions/"+x.ID+"/")) {
				return reject(ctx, tx, b, "missing measurement evidence")
			}
			if x.Outcome == lab.OutcomeRunning || x.FinishedAt == nil || (c.Device == lab.PlateReaderID && x.Outcome == lab.OutcomeCompleted && x.DataState != lab.Stored && x.DataState != lab.Lost) {
				return reject(ctx, tx, b, "unproven terminal report")
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO inbox(id,payload) VALUES($1,$2)", rep.ID, b); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE attempts SET report=$1 WHERE job_id=$2", b, c.JobID); err != nil {
			return err
		}
		for i := range r.View.Steps {
			s := &r.View.Steps[i]
			if s.Name != c.Step {
				continue
			}
			st := r.State[s.Name]
			if st.Job != c.JobID {
				continue
			}
			s.Error = ptr(rep.Error)
			for _, x := range rep.Executions {
				if s.StartedAt == nil || x.StartedAt.Before(*s.StartedAt) {
					start := x.StartedAt
					s.StartedAt = &start
				}
			}
			if rep.Terminal {
				x := rep.Executions[0]
				s.FinishedAt = x.FinishedAt
				st.Job = ""
				if _, err = tx.ExecContext(ctx, "DELETE FROM reservations WHERE job_id=$1", c.JobID); err != nil {
					return err
				}
				switch {
				case x.Outcome == lab.OutcomeCompleted && x.DataState != lab.Lost:
					s.Status = StepCompleted
					s.DataRef = ptr(x.DataRef)
					s.Error = nil
				case !r.Cancel && lab.CanRetry(c.Device, c.Attempt, x.Outcome) && x.Retryable:
					s.Status = StepRunning
					due := time.Now().UTC().Add(lab.Backoff(c.Attempt - 1))
					st.RetryAt = &due
					s.FinishedAt = nil
					s.Error = ptr(x.Error)
				default:
					s.Status = StepFailed
					s.Error = ptr(x.Error)
					if x.DataState == lab.Lost {
						s.Error = ptr(lab.MeasurementDataLost)
					}
				}
			}
		}
		settle(&r)
		return saveRecord(ctx, tx, r)
	})
}
func reject(ctx context.Context, tx *sql.Tx, b []byte, reason string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO rejections(id,payload,reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", "reject-"+fmt.Sprintf("%x", hashBytes(b)), b, reason)
	return err
}
