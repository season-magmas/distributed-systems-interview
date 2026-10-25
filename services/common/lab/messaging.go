package lab

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/nats-io/nats.go"
)

const StreamCapacity = 64 << 20

func Provision(nc *nats.Conn) (nats.JetStreamContext, error) {
	js, err := nc.JetStream(nats.MaxWait(2 * time.Second))
	if err != nil {
		return nil, err
	}
	for _, spec := range []struct{ name, subject string }{{CommandStream, CommandPrefix + "*"}, {ResultStream, ResultPrefix + "*"}} {
		cfg := nats.StreamConfig{Name: spec.name, Subjects: []string{spec.subject}, Storage: nats.FileStorage, Retention: nats.WorkQueuePolicy, Discard: nats.DiscardNew, MaxBytes: StreamCapacity}
		info, e := js.StreamInfo(spec.name)
		if errors.Is(e, nats.ErrStreamNotFound) {
			_, e = js.AddStream(&cfg)
		} else if e == nil && (info.Config.Retention != cfg.Retention || info.Config.Storage != cfg.Storage || info.Config.Discard != cfg.Discard || !reflect.DeepEqual(info.Config.Subjects, cfg.Subjects) || info.Config.MaxAge != 0 || info.Config.MaxBytes != cfg.MaxBytes) {
			e = fmt.Errorf("incompatible stream %s", spec.name)
		}
		if e != nil {
			return nil, e
		}
	}
	for _, d := range Devices {
		if err = consumer(js, CommandStream, d, CommandPrefix+d); err != nil {
			return nil, err
		}
	}
	if err = consumer(js, ResultStream, ExecutorConsumer, ResultPrefix+"*"); err != nil {
		return nil, err
	}
	return js, nil
}
func consumer(js nats.JetStreamContext, stream, name, filter string) error {
	info, err := js.ConsumerInfo(stream, name)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		_, err = js.AddConsumer(stream, &nats.ConsumerConfig{Durable: name, AckPolicy: nats.AckExplicitPolicy, FilterSubject: filter, AckWait: AckWait, MaxAckPending: 256, MaxDeliver: -1})
		return err
	}
	if err != nil {
		return err
	}
	if info.Config.FilterSubject != filter || info.Config.AckPolicy != nats.AckExplicitPolicy {
		return fmt.Errorf("incompatible consumer %s", name)
	}
	return nil
}
func Publish(ctx context.Context, js nats.JetStreamContext, subject, id string, data []byte) error {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := js.Publish(subject, data, nats.MsgId(id), nats.Context(call))
	return err
}
func Wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
