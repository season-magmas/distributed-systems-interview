package lab

import (
	"context"
	"encoding/json"
	"github.com/nats-io/nats.go"
	"os"
	"testing"
	"time"
)

func TestIntegrationDurableRedelivery(t *testing.T) {
	url := os.Getenv("LAB_TEST_NATS")
	if url == "" {
		t.Skip("set LAB_TEST_NATS for broker integration")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := Provision(nc)
	if err != nil {
		t.Fatal(err)
	}
	// The integration stack is disposable and this test runs before executor tests.
	if err = js.PurgeStream(CommandStream); err != nil {
		t.Fatal(err)
	}
	subject := CommandPrefix + Devices[0]
	durable := Devices[0]
	sub, err := js.PullSubscribe(subject, durable, nats.Bind(CommandStream, durable))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload, _ := json.Marshal(map[string]string{"job_id": "transport-job"})
	if err = Publish(ctx, js, subject, "transport-message", payload); err != nil {
		t.Fatal(err)
	}
	msgs, err := sub.Fetch(1, nats.MaxWait(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = msgs[0].Nak(); err != nil {
		t.Fatal(err)
	}
	sub.Unsubscribe()
	sub, err = js.PullSubscribe(subject, durable, nats.Bind(CommandStream, durable))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	msgs, err = sub.Fetch(1, nats.MaxWait(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if string(msgs[0].Data) != string(payload) {
		t.Fatal("redelivery changed identity")
	}
	if err = msgs[0].AckSync(); err != nil {
		t.Fatal(err)
	}
}
