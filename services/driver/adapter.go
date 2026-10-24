package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/automata-tech/distributed-systems-interview/services/common/storage"
	"github.com/automata-tech/distributed-systems-interview/services/driver/instrument"
	"github.com/nats-io/nats.go"
)

func Serve(ctx context.Context, inst *instrument.Instrument, nc *nats.Conn, st *storage.Storage) error {
	dir := os.Getenv("SPOOL_DIR")
	if dir == "" {
		dir = "/spool"
	}
	spool, err := lab.OpenSpool(dir)
	if err != nil {
		return err
	}
	defer spool.Close()
	var js nats.JetStreamContext
	for ctx.Err() == nil {
		js, err = nc.JetStream(nats.MaxWait(2 * time.Second))
		if err == nil {
			_, err = js.ConsumerInfo(lab.CommandStream, inst.DeviceID())
		}
		if err == nil {
			break
		}
		if !lab.Wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
	sub, err := js.PullSubscribe(lab.CommandPrefix+inst.DeviceID(), inst.DeviceID(), nats.Bind(lab.CommandStream, inst.DeviceID()))
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	w := worker{inst: inst, spool: spool, store: st}
	for ctx.Err() == nil {
		msgs, err := sub.Fetch(1, nats.MaxWait(time.Second))
		if err != nil {
			if !lab.Wait(ctx, lab.PollInterval) {
				break
			}
			continue
		}
		for _, m := range msgs {
			var c lab.Command
			if err = json.Unmarshal(m.Data, &c); err != nil || c.Validate() != nil || c.Device != inst.DeviceID() {
				name := fmt.Sprintf("rejected-%x", sha256.Sum256(m.Data))
				if err = spool.Save(name, map[string]any{"payload": m.Data, "reason": "invalid command"}); err != nil {
					return err
				}
				m.Ack()
				continue
			}
			heartbeat, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for lab.Wait(heartbeat, lab.Heartbeat) {
					if err := m.InProgress(); err != nil {
						log.Printf("command heartbeat: %v", err)
					}
				}
			}()
			_, err = w.run(ctx, c, func(r lab.Report) error { return lab.Publish(ctx, js, lab.ResultPrefix+c.Device, r.ID, reportBytes(r)) })
			cancel()
			<-done
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if err = m.Ack(); err != nil {
				log.Printf("command ack: %v", err)
			}
		}
	}
	return ctx.Err()
}
