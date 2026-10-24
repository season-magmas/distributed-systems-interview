package lab

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const SpoolLimit int64 = 64 << 20

type Spool struct {
	Dir  string
	lock *os.File
}

func OpenSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("spool already owned: %w", err)
	}
	return &Spool{Dir: dir, lock: f}, nil
}
func (s *Spool) Close() error { return s.lock.Close() }
func (s *Spool) Save(name string, value any) error {
	if !SafeID(name) {
		return errors.New("invalid spool identity")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	var size int64
	for _, e := range entries {
		i, e := e.Info()
		if e != nil {
			return e
		}
		size += i.Size()
	}
	if size+int64(len(b)) > SpoolLimit {
		return errors.New("spool capacity exhausted")
	}
	f, err := os.CreateTemp(s.Dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(s.Dir, name+".json")); err != nil {
		return err
	}
	d, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s *Spool) Load(name string, value any) error {
	if !SafeID(name) {
		return errors.New("invalid spool identity")
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, name+".json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}
