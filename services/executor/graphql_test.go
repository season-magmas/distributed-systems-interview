package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func graphRequest(a *API, query string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"query": query})
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	a.Routes().ServeHTTP(w, req)
	return w
}
func TestGraphQLSchemaAndValidation(t *testing.T) {
	a := &API{workflowsPath: "../../config/workflows.yaml"}
	response := graphRequest(a, `{ __schema { queryType { name } mutationType { name } } }`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"Mutation"`) {
		t.Fatalf("missing GraphQL schema: %d %s", response.Code, response.Body.String())
	}
	response = graphRequest(a, `mutation { startRun { id } }`)
	if !strings.Contains(response.Body.String(), `"errors"`) {
		t.Fatal("invalid mutation was accepted")
	}
	response = graphRequest(a, `{ workflows { name isDefault steps { deviceId dependsOn } } }`)
	if strings.Contains(response.Body.String(), `"errors"`) || !strings.Contains(response.Body.String(), "PCR Amplification") {
		t.Fatal(response.Body.String())
	}
}
func TestIntegrationGraphQLMutationsAndReadModel(t *testing.T) {
	e, _ := integrationExecutor(t)
	a := &API{executor: e, workflowsPath: "../../config/workflows.yaml"}
	response := graphRequest(a, `mutation { createRun { id status steps { attempts { jobId } } } }`)
	var body struct {
		Data   struct{ CreateRun struct{ ID string } }
		Errors []any
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Errors) > 0 || body.Data.CreateRun.ID == "" {
		t.Fatal(response.Body.String())
	}
	id := body.Data.CreateRun.ID
	response = graphRequest(a, `mutation { cancelRun(id:"`+id+`") { status cancellationRequested steps { status attempts { executions { outcome measurement { state } } } } } }`)
	if strings.Contains(response.Body.String(), `"errors"`) || !strings.Contains(response.Body.String(), "CANCELLED") {
		t.Fatal(response.Body.String())
	}
	response = graphRequest(a, `{ run(id:"absent") { id } }`)
	if !strings.Contains(response.Body.String(), `"run":null`) {
		t.Fatal(response.Body.String())
	}
}

func TestGraphQLUnknownWorkflowIsActionable(t *testing.T) {
	a := &API{workflowsPath: "../../config/workflows.yaml"}
	response := graphRequest(a, `mutation { createRun(workflowName:"missing-assay") { id } }`)
	if !strings.Contains(response.Body.String(), "unknown workflow") {
		t.Fatalf("unhelpful domain error: %s", response.Body.String())
	}
}
