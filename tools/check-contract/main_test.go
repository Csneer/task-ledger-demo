package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractCheckerRejectsBrokenArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any, *[]route, *vectors)
		files  func(*testing.T, string)
		want   string
	}{
		{name: "valid artifacts"},
		{name: "missing route", mutate: func(_ map[string]any, routes *[]route, _ *vectors) {
			*routes = (*routes)[:2]
		}, want: "exactly 3 routes"},
		{name: "duplicate id", mutate: func(_ map[string]any, _ *[]route, data *vectors) {
			data.Scenarios[0].ID = data.Cases[0].ID
		}, want: "duplicate vector/scenario id"},
		{name: "wrong expected validity", mutate: func(_ map[string]any, _ *[]route, data *vectors) {
			no := false
			data.Cases[0].Valid = &no
		}, want: "expected valid=false"},
		{name: "missing positive operation", mutate: func(_ map[string]any, _ *[]route, data *vectors) {
			data.Cases = data.Cases[1:]
		}, want: "missing positive operation cases: task.create"},
		{name: "unregistered reference", mutate: func(schema map[string]any, _ *[]route, _ *vectors) {
			schema["$defs"].(map[string]any)["Broken"] = map[string]any{"$ref": "https://invalid.example/schema"}
		}, want: "network loading disabled"},
		{name: "malformed behavior request", mutate: func(_ map[string]any, _ *[]route, data *vectors) {
			data.Scenarios[0].Steps[0].Request = map[string]any{"operation": "unknown"}
		}, want: "request shape"},
		{name: "non-route behavior definition", mutate: func(_ map[string]any, _ *[]route, data *vectors) {
			data.Scenarios[0].Steps[0].Definition = "CommandResponse"
		}, want: "non-route request"},
		{name: "missing operation catalog entry", files: func(t *testing.T, dir string) {
			writeJSON(t, filepath.Join(dir, "operations.json"), []operationMapping{})
		}, want: "operations.json covers 0 of 3"},
		{name: "error catalog mismatch", files: func(t *testing.T, dir string) {
			writeJSON(t, filepath.Join(dir, "errors.json"), map[string]map[string]int{"binding": {"TYPO": 400}, "core": {"INVALID_MESSAGE": 400}})
		}, want: "missing or has invalid HTTP status"},
		{name: "scenario error status mismatch", mutate: func(_ map[string]any, _ *[]route, data *vectors) {
			data.Scenarios[0].Steps[0].Expected.HTTPStatus = 409
			data.Scenarios[0].Steps[0].Expected.ErrorScope = "binding"
			data.Scenarios[0].Steps[0].Expected.ErrorCode = "INVALID_MESSAGE"
		}, want: "differs from errors.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema, core, routes, data := fixture(t)
			if tc.mutate != nil {
				tc.mutate(schema, &routes, &data)
			}
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "schema.json"), filepath.Join(dir, "core.json"), filepath.Join(dir, "routes.json"), filepath.Join(dir, "vectors.json")}
			for i, value := range []any{schema, core, routes, data} {
				writeJSON(t, paths[i], value)
			}
			writeJSON(t, filepath.Join(dir, "operations.json"), []operationMapping{
				{Operation: "task.create", Route: "/v1/commands", Request: "CommandRequest", Response: "CommandResponse", Authorization: "writer"},
				{Operation: "action.one", Route: "/v1/actions", Request: "ActionRequest", Response: "ActionResponse", Authorization: "writer"},
				{Operation: "query.one", Route: "/v1/queries", Request: "QueryRequest", Response: "QueryResponse", Authorization: "reader"},
			})
			writeJSON(t, filepath.Join(dir, "errors.json"), map[string]map[string]int{"binding": {"INVALID_MESSAGE": 400}, "core": {"INVALID_MESSAGE": 400}})
			if tc.files != nil {
				tc.files(t, dir)
			}
			got, err := check(paths[0], paths[1], paths[2], paths[3])
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("wanted error containing %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Routes != 3 || got.Cases != 4 || got.Operations != 3 || got.Scenarios != 1 || got.Steps != 1 {
				t.Fatalf("unexpected static report: %+v", got)
			}
		})
	}
}

func TestArtifactJSONRejectsAmbiguityAndPreservesNumbers(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"a":[{"x":1,"x":2}]}`, `{} {}`, `{"a":`} {
		if err := rejectDuplicateKeys([]byte(raw)); err == nil {
			t.Errorf("accepted ambiguous/invalid artifact %s", raw)
		}
	}
	path := filepath.Join(t.TempDir(), "numbers.json")
	if err := os.WriteFile(path, []byte(`{"value":0.10000000000000000000000000001}`), 0600); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := readTyped(path, &parsed); err != nil {
		t.Fatal(err)
	}
	if got, ok := parsed["value"].(json.Number); !ok || got.String() != "0.10000000000000000000000000001" {
		t.Fatalf("artifact number lost precision: %#v", parsed["value"])
	}
}

func fixture(t *testing.T) (map[string]any, map[string]any, []route, vectors) {
	t.Helper()
	var schema, core map[string]any
	if err := json.Unmarshal([]byte(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "$id":"urn:task-ledger:ledger-http-draft-1",
  "$defs":{
    "CommandRequest":{"type":"object","properties":{"command":{"allOf":[{"$ref":"urn:agent-task-bridge:task-core-draft-1#/$defs/Command"},{"properties":{"operation":{"const":"task.create"}}}]}},"required":["command"],"additionalProperties":false},
    "ActionRequest":{"oneOf":[{"$ref":"#/$defs/Action_one"}]},
    "Action_one":{"type":"object","properties":{"operation":{"const":"action.one"}},"required":["operation"],"additionalProperties":false},
    "QueryRequest":{"type":"object","properties":{"operation":{"const":"query.one"}},"required":["operation"],"additionalProperties":false},
    "CommandResponse":{"type":"object"},
    "ActionResponse":{"type":"object"},
    "QueryResponse":{"type":"object"},
    "BindingError":{"type":"object","properties":{"code":{"enum":["INVALID_MESSAGE"]}}}
  }
}`), &schema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:agent-task-bridge:task-core-draft-1","$defs":{"Command":{"type":"object","properties":{"operation":{"enum":["task.create"]}},"required":["operation"]},"Error":{"type":"object","properties":{"kind":{"enum":["INVALID_MESSAGE"]}}}}}`), &core); err != nil {
		t.Fatal(err)
	}
	routes := []route{
		{Method: "POST", Path: "/v1/commands", Request: "CommandRequest", Response: "CommandResponse"},
		{Method: "POST", Path: "/v1/actions", Request: "ActionRequest", Response: "ActionResponse"},
		{Method: "POST", Path: "/v1/queries", Request: "QueryRequest", Response: "QueryResponse"},
	}
	var data vectors
	if err := json.Unmarshal([]byte(`{
  "cases":[
    {"id":"create","definition":"CommandRequest","valid":true,"value":{"command":{"operation":"task.create"}}},
    {"id":"action","definition":"ActionRequest","valid":true,"value":{"operation":"action.one"}},
    {"id":"query","definition":"QueryRequest","valid":true,"value":{"operation":"query.one"}},
    {"id":"invalid","definition":"CommandRequest","valid":false,"value":{}}
  ],
  "scenarios":[{"id":"behavior","scope":"design-only","description":"Expected outcome is not executed","steps":[{"request_definition":"CommandRequest","request":{"command":{"operation":"task.create"}},"expected":{"http_status":200}}],"invariants":["No runtime execution is implied"]}]
}`), &data); err != nil {
		t.Fatal(err)
	}
	return schema, core, routes, data
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
