package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScientistUIRoutes(t *testing.T) {
	routes := (&API{workflowsPath: "../../config/workflows.yaml"}).Routes()
	for _, tc := range []struct {
		path, contentType, contains string
		code                        int
	}{
		{"/", "text/html", "Lab notebook", 200},
		{"/ui/app.mjs", "javascript", "/graphql", 200},
		{"/ui/style.css", "text/css", "@media", 200},
		{"/ui/missing.js", "text/plain", "404", 404},
		{"/not-an-api", "text/plain", "404", 404},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.code || !strings.Contains(w.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("%s: %d %v %s", tc.path, w.Code, w.Header(), w.Body.String())
			}
		})
	}
}
