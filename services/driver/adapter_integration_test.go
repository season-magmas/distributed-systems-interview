package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/automata-tech/distributed-systems-interview/services/common/storage"
	"github.com/automata-tech/distributed-systems-interview/services/driver/instrument"
	"github.com/nats-io/nats.go"
)

func TestIntegrationAdapterDurableRoundTrip(t *testing.T) {
	url, endpoint := os.Getenv("LAB_TEST_NATS"), os.Getenv("LAB_TEST_S3")
	if url == "" || endpoint == "" {
		t.Skip("set LAB_TEST_NATS and LAB_TEST_S3 for isolated integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := lab.Provision(nc)
	if err != nil {
		t.Fatal(err)
	}
	st, err := storage.New(endpoint, "test-data")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if err = st.EnsureBucket(ctx); err == nil {
			break
		}
		if !lab.Wait(ctx, lab.PollInterval) {
			t.Fatal(err)
		}
	}
	inst, err := instrument.New(instrument.Config{DeviceID: "plate-reader-1", LedgerDir: t.TempDir(), ProducesData: true, FailuresRetryable: true, Duration: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPOOL_DIR", t.TempDir())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, inst, nc, st) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("adapter shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("adapter did not stop")
		}
	}()
	sub, err := js.PullSubscribe(lab.ResultPrefix+"*", "executor", nats.Bind(lab.ResultStream, "executor"))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	now := time.Now().UTC()
	c := lab.Command{Version: lab.Version, ID: "driver-test-command", RunID: "driver-test-run", Step: "read", Device: inst.DeviceID(), JobID: "driver-test-job", Attempt: 1, CreatedAt: now, DispatchDeadline: now.Add(lab.DispatchTimeout), RecoveryTimeout: lab.DispatchTimeout}
	payload, _ := json.Marshal(c)
	if err = lab.Publish(ctx, js, lab.CommandPrefix+c.Device, c.ID, payload); err != nil {
		t.Fatal(err)
	}
	var final lab.Report
	for !final.Terminal && ctx.Err() == nil {
		messages, err := sub.Fetch(1, nats.MaxWait(time.Second))
		if errors.Is(err, nats.ErrTimeout) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			var r lab.Report
			if err = json.Unmarshal(message.Data, &r); err != nil {
				t.Fatal(err)
			}
			if r.Command.JobID == c.JobID {
				final = r
			}
			if err = message.AckSync(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !final.Terminal || len(final.Executions) != 1 || final.Executions[0].DataState != lab.Stored {
		t.Fatalf("missing durable result: %+v", final)
	}
	x := final.Executions[0]
	bytes, err := st.Get(ctx, strings.TrimPrefix(x.DataRef, "s3://test-data/"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(bytes)) != x.Hash {
		t.Fatalf("wrong measurement: %v", err)
	}
	// Bypass the broker's publication deduplication to exercise adapter deduplication.
	if _, err = js.Publish(lab.CommandPrefix+c.Device, payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := js.ConsumerInfo(lab.CommandStream, c.Device)
		if err != nil {
			t.Fatal(err)
		}
		if info.NumAckPending == 0 && info.NumPending == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(inst.Status().Executions) != 1 {
		t.Fatal("duplicate delivery repeated physical work")
	}
}
