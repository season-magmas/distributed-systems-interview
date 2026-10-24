package main

import (
	"context"
	"errors"
	"github.com/automata-tech/distributed-systems-interview/services/common/lab"
	"github.com/automata-tech/distributed-systems-interview/services/driver/instrument"
)

type objectStore interface {
	Put(context.Context, string, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
	Exists(context.Context, string) (bool, error)
}
type worker struct {
	inst  *instrument.Instrument
	spool *lab.Spool
	store objectStore
}

func (w *worker) run(ctx context.Context, c lab.Command, publish func(lab.Report) error) (lab.Report, error) {
	return lab.Report{}, errors.New("not implemented")
}
