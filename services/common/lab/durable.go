package lab

import (
	"errors"
	"os"
)

type Spool struct {
	Dir  string
	lock *os.File
}

func OpenSpool(dir string) (*Spool, error)         { return nil, errors.New("not implemented") }
func (s *Spool) Close() error                      { return nil }
func (s *Spool) Save(name string, value any) error { return errors.New("not implemented") }
func (s *Spool) Load(name string, value any) error { return errors.New("not implemented") }
