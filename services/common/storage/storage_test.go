package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestObjectRoundTripAndMissingObject(t *testing.T) {
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test" && r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = b
			w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet, http.MethodHead:
			if b, ok := objects[r.URL.Path]; ok {
				w.Header().Set("Content-Length", fmt.Sprint(len(b)))
				w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
				if r.Method == http.MethodGet {
					w.Write(b)
				}
			} else {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			}
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	s, err := New(server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	exists, err := s.Exists(ctx, "missing")
	if err != nil || exists {
		t.Fatalf("missing object: %v %v", exists, err)
	}
	ref, err := s.Put(ctx, "measurement.csv", []byte("well,absorbance\nA1,1.0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ref != "s3://test/measurement.csv" {
		t.Fatal(ref)
	}
	b, err := s.Get(ctx, "measurement.csv")
	if err != nil || !strings.Contains(string(b), "A1,1.0") {
		t.Fatalf("round trip: %s %v", b, err)
	}
}
