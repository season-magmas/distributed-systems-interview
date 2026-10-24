package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPErrorContracts(t *testing.T) {
	for _, tc := range []struct {
		err      error
		code     int
		contains string
	}{{ErrRunNotFound, 404, "run not found"}, {ErrNotImplemented, 501, "not implemented"}, {errors.New("private database details"), 500, "internal error"}} {
		w := httptest.NewRecorder()
		writeError(w, tc.err)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private") {
			t.Fatal("internal error leaked")
		}
	}
	a := &API{workflowsPath: "../../config/workflows.yaml"}
	w := httptest.NewRecorder()
	a.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader("{")))
	if w.Code != http.StatusBadRequest {
		t.Fatal("malformed body accepted")
	}
	w = httptest.NewRecorder()
	a.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/graphql", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("GET mutation transport allowed")
	}
	origin := time.Now()
	if offsetMs(nil, origin) != nil {
		t.Fatal("invented physical timestamp")
	}
	finish := origin.Add(2 * time.Second)
	if *offsetMs(&finish, origin) != 2000 {
		t.Fatal("incorrect timeline")
	}
}
