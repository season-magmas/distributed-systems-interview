package lab

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpoolExclusiveAndPersistent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := OpenSpool(dir); e == nil {
		other.Close()
		t.Fatal("two spool owners")
	}
	value := map[string]string{"bytes": "real measurement"}
	if err = s.Save("job-1", value); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got map[string]string
	if err = s.Load("job-1", &got); err != nil || got["bytes"] != value["bytes"] {
		t.Fatalf("lost durable record: %v %v", got, err)
	}
	if s.Save("../outside", value) == nil {
		t.Fatal("path traversal accepted")
	}
	if err = os.WriteFile(filepath.Join(dir, "job-2.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.Load("job-2", &got) == nil {
		t.Fatal("corruption ignored")
	}
}
