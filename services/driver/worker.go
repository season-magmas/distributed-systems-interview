package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/automata-tech/distributed-systems-interview/services/driver/instrument"
)

type objectStore interface {
	Ref(string) string
	Put(context.Context, string, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
	Exists(context.Context, string) (bool, error)
}
type worker struct {
	inst  *instrument.Instrument
	spool *lab.Spool
	store objectStore
}
type durableJob struct {
	Command lab.Command
	Report  lab.Report
	Bytes   []byte
}
type result struct {
	out instrument.Outcome
	err error
}

func stamp(t float64) time.Time { return time.Unix(0, int64(t*1e9)).UTC() }
func (w *worker) run(ctx context.Context, c lab.Command, publish func(lab.Report) error) (lab.Report, error) {
	if err := c.Validate(); err != nil {
		return lab.Report{}, err
	}
	if c.Device != w.inst.DeviceID() {
		return lab.Report{}, errors.New("wrong device")
	}
	var job durableJob
	err := w.spool.Load(c.JobID, &job)
	if errors.Is(err, os.ErrNotExist) {
		job.Command = c
		if err = w.spool.Save(c.JobID, job); err != nil {
			return job.Report, err
		}
	} else if err != nil {
		return job.Report, err
	}
	if !reflect.DeepEqual(job.Command, c) {
		return job.Report, errors.New("command identity conflict")
	}
	if job.Report.Terminal {
		return job.Report, publish(job.Report)
	}
	var waiting bool
	var receiver chan result
	var ready chan struct{}
	observations := 0
	var waitCancel context.CancelFunc
	defer func() {
		if waitCancel != nil {
			waitCancel()
		}
	}()
	delay := lab.PollInterval
	failures := 0
	for ctx.Err() == nil {
		status := w.inst.Status()
		var executions []instrument.Execution
		for _, x := range status.Executions {
			if x.JobID == c.JobID {
				executions = append(executions, x)
			}
		}
		if ledgerConflict(job.Report.Executions, executions) {
			if job.Report.Error != "spool and ledger evidence conflict" {
				job.Report.Error = "spool and ledger evidence conflict"
				job.Report.Terminal = false
				job.Report.Revision++
				job.Report.ID = fmt.Sprintf("report-%s-%d", c.JobID, job.Report.Revision)
				if err = w.spool.Save(c.JobID, job); err != nil {
					return job.Report, err
				}
			}
			if err = publish(job.Report); err != nil {
				return job.Report, err
			}
			return job.Report, errors.New(job.Report.Error)
		}
		if len(executions) == 0 && !status.Busy && !waiting {
			waiting = true
			receiver = make(chan result, 1)
			ready = make(chan struct{})
			call, cancel := context.WithCancel(ctx)
			waitCancel = cancel
			go func(output chan result, signal chan struct{}) {
				defer close(signal)
				out, err := w.inst.Run(call, instrument.Job{JobID: c.JobID, RunID: c.RunID, StepName: c.Step})
				output <- result{out, err}
			}(receiver, ready)
		}
		if waiting {
			select {
			case got := <-receiver:
				waiting = false
				receiver = nil
				ready = nil
				if got.err != nil {
					if ctx.Err() != nil {
						return job.Report, ctx.Err()
					}
					if !errors.Is(got.err, instrument.ErrBusy) {
						return job.Report, got.err
					}
				}
				if len(got.out.Data) > 0 {
					job.Bytes = got.out.Data
					if err = w.spool.Save(c.JobID, job); err != nil {
						return job.Report, err
					}
				}
			default:
			}
		}
		next := lab.Report{Version: lab.Version, Command: c, Executions: []lab.Execution{}}
		for _, x := range executions {
			physical := lab.Execution{ID: x.ExecutionID, Outcome: x.Outcome, StartedAt: stamp(x.StartedAt), Error: x.Error, Retryable: w.inst.FailuresRetryable(), DataState: lab.NotApplicable}
			if x.FinishedAt != nil {
				finish := stamp(*x.FinishedAt)
				physical.FinishedAt = &finish
			}
			window := recoveryWindow(c.RecoveryTimeout, w.inst.Duration(), duration("HANG_DURATION", "30s"))
			deadline := physical.StartedAt.Add(window)
			if next.RecoveryDeadline == nil {
				next.RecoveryDeadline = &deadline
			}
			if x.DataSHA256 != nil {
				physical.Hash = *x.DataSHA256
			}
			if w.inst.ProducesData() && x.Outcome == instrument.OutcomeRunning {
				physical.DataState = lab.Pending
			}
			if w.inst.ProducesData() && x.Outcome == instrument.OutcomeCompleted {
				physical.DataState = lab.Pending
				if waiting {
					next.Error = "measurement capture pending"
				} else {
					if err = w.storeMeasurement(ctx, &job, &physical); err != nil {
						next.Error = err.Error()
						physical.DataError = err.Error()
					}
				}
			}
			next.Executions = append(next.Executions, physical)
		}
		if len(executions) == 1 {
			x := next.Executions[0]
			if x.Outcome != instrument.OutcomeRunning && x.FinishedAt != nil && (x.DataState != lab.Pending) {
				next.Terminal = true
				if x.Outcome == instrument.OutcomeHung && waitCancel != nil {
					waitCancel()
				}
			}
		}
		if len(executions) > 1 {
			next.Error = "multiple physical executions for one job"
			next.Terminal = false
		}
		if !next.Terminal && next.RecoveryDeadline != nil && time.Now().After(*next.RecoveryDeadline) {
			next.Error = "recovery unresolved: deadline exceeded"
		}
		old := job.Report
		comparable := next
		comparable.ID = old.ID
		comparable.Revision = old.Revision
		if !reflect.DeepEqual(comparable, old) {
			next.Revision = old.Revision + 1
			next.ID = fmt.Sprintf("report-%s-%d", c.JobID, next.Revision)
			job.Report = next
			if err = w.spool.Save(c.JobID, job); err != nil {
				return old, err
			}
		}
		if err = publish(job.Report); err != nil {
			failures++
			delay = lab.Backoff(failures - 1)
		} else {
			failures = 0
			delay = lab.Backoff(observations)
			observations++
			if job.Report.Terminal {
				return job.Report, nil
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return job.Report, ctx.Err()
		case <-ready:
			timer.Stop()
		case <-timer.C:
		}

	}
	return job.Report, ctx.Err()
}
func (w *worker) storeMeasurement(ctx context.Context, job *durableJob, x *lab.Execution) error {
	if w.store == nil {
		return errors.New("object storage unavailable")
	}
	key := fmt.Sprintf("runs/%s/jobs/%s/executions/%s/measurement.csv", job.Command.RunID, job.Command.JobID, x.ID)
	exists, err := w.store.Exists(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		b, err := w.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != x.Hash {
			return errors.New("measurement hash conflict")
		}
		x.DataState = lab.Stored
		x.DataRef = w.store.Ref(key)
		return nil
	}
	if len(job.Bytes) == 0 {
		x.DataState = lab.Lost
		x.DataError = lab.MeasurementDataLost
		return nil
	}
	if fmt.Sprintf("%x", sha256.Sum256(job.Bytes)) != x.Hash {
		return errors.New("captured measurement hash conflict")
	}
	ref, err := w.store.Put(ctx, key, job.Bytes)
	if err != nil {
		return err
	}
	stored, err := w.store.Get(ctx, key)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(stored)) != x.Hash {
		return errors.New("stored measurement hash conflict")
	}
	x.DataRef = ref
	x.DataState = lab.Stored
	return nil
}
func reportBytes(r lab.Report) []byte { b, _ := json.Marshal(r); return b }

func ledgerConflict(previous []lab.Execution, current []instrument.Execution) bool {
	for _, old := range previous {
		found := false
		for _, physical := range current {
			if old.ID != physical.ExecutionID {
				continue
			}
			found = true
			if !old.StartedAt.Equal(stamp(physical.StartedAt)) || (old.Outcome != instrument.OutcomeRunning && old.Outcome != physical.Outcome) {
				return true
			}
			if old.Hash != "" && (physical.DataSHA256 == nil || *physical.DataSHA256 != old.Hash) {
				return true
			}
		}
		if !found {
			return true
		}
	}
	return false
}
