// Command check-contract validates the S1 contract artifacts only. It does not
// execute task operations, HTTP traffic, identity checks, or behavior scenarios.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	bindingID = "urn:task-ledger:ledger-http-draft-1"
	coreID    = "urn:agent-task-bridge:task-core-draft-1"
)

type route struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Request  string `json:"request"`
	Response string `json:"response"`
}

type operationMapping struct {
	Operation     string `json:"operation"`
	Route         string `json:"route"`
	Request       string `json:"request"`
	Response      string `json:"response"`
	Authorization string `json:"authorization"`
}

type vector struct {
	ID         string `json:"id"`
	Definition string `json:"definition"`
	Valid      *bool  `json:"valid"`
	Value      any    `json:"value"`
}

type scenario struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Description string `json:"description,omitempty"`
	Steps       []struct {
		Definition string `json:"request_definition"`
		Request    any    `json:"request"`
		Expected   struct {
			HTTPStatus int    `json:"http_status"`
			ErrorCode  string `json:"error_code,omitempty"`
			ErrorScope string `json:"error_scope,omitempty"`
		} `json:"expected"`
	} `json:"steps"`
	Invariants []string `json:"invariants"`
}

type vectors struct {
	Cases     []vector   `json:"cases"`
	Scenarios []scenario `json:"scenarios"`
}

type report struct {
	Definitions     int
	CoreDefinitions int
	Routes          int
	Cases           int
	Operations      int
	Scenarios       int
	Steps           int
}

// All schema resources are registered explicitly. A missing reference must
// fail locally rather than fetch a changed or unavailable remote document.
type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("unregistered schema resource %q (network loading disabled)", url)
}

func main() {
	r, err := check("contract/v1/schema.json", "protocol/upstream/schema.json", "contract/v1/routes.json", "contract/v1/vectors.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("PASS static contract: %d binding / %d upstream definitions, %d routes, %d structural vectors, %d operation positive cases covered\n", r.Definitions, r.CoreDefinitions, r.Routes, r.Cases, r.Operations)
	fmt.Printf("CHECKED SHAPES ONLY: %d behavior scenarios / %d requests; expected outcomes and invariants NOT EXECUTED\n", r.Scenarios, r.Steps)
	fmt.Println("NOT RUN: HTTP service, task state machine, identity/session enforcement, persistence, launcher, interoperability, or load tests.")
}

func check(schemaPath, corePath, routesPath, vectorsPath string) (report, error) {
	var result report
	binding, err := readDocument(schemaPath, bindingID)
	if err != nil {
		return result, err
	}
	core, err := readDocument(corePath, coreID)
	if err != nil {
		return result, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(offlineLoader{})
	for id, doc := range map[string]map[string]any{bindingID: binding, coreID: core} {
		if err := compiler.AddResource(id, doc); err != nil {
			return result, err
		}
	}
	definitions, ok := binding["$defs"].(map[string]any)
	if !ok || len(definitions) == 0 {
		return result, errors.New("binding schema must contain nonempty $defs")
	}
	compiled := make(map[string]*jsonschema.Schema, len(definitions))
	for name := range definitions {
		schema, err := compiler.Compile(bindingID + "#/$defs/" + pointerEscape(name))
		if err != nil {
			return result, fmt.Errorf("compile definition %s: %w", name, err)
		}
		compiled[name] = schema
	}
	result.Definitions = len(compiled)
	coreDefinitions, ok := core["$defs"].(map[string]any)
	if !ok || len(coreDefinitions) == 0 {
		return result, errors.New("upstream schema must contain nonempty $defs")
	}
	for name := range coreDefinitions {
		if _, err := compiler.Compile(coreID + "#/$defs/" + pointerEscape(name)); err != nil {
			return result, fmt.Errorf("compile upstream definition %s: %w", name, err)
		}
	}
	result.CoreDefinitions = len(coreDefinitions)
	var routes []route
	if err := readTyped(routesPath, &routes); err != nil {
		return result, err
	}
	if err := validateRoutes(routes, compiled); err != nil {
		return result, err
	}
	result.Routes = len(routes)
	var input vectors
	if err := readTyped(vectorsPath, &input); err != nil {
		return result, err
	}
	if len(input.Cases) == 0 || len(input.Scenarios) == 0 {
		return result, errors.New("vectors must include structural cases and behavior scenario specifications")
	}
	seen := make(map[string]bool)
	covered := make(map[string]map[string]bool)
	for _, tc := range input.Cases {
		if err := uniqueID(seen, tc.ID); err != nil {
			return result, err
		}
		schema, ok := compiled[tc.Definition]
		if !ok {
			return result, fmt.Errorf("case %s references unknown definition %q", tc.ID, tc.Definition)
		}
		if tc.Valid == nil {
			return result, fmt.Errorf("case %s requires a boolean valid field", tc.ID)
		}
		err := schema.Validate(tc.Value)
		if (err == nil) != *tc.Valid {
			return result, fmt.Errorf("case %s: expected valid=%t, validation error=%v", tc.ID, *tc.Valid, err)
		}
		if *tc.Valid {
			if covered[tc.Definition] == nil {
				covered[tc.Definition] = make(map[string]bool)
			}
			covered[tc.Definition][operation(tc.Value)] = true
		}
	}
	result.Cases = len(input.Cases)
	documents := map[string]map[string]any{bindingID: binding, coreID: core}
	for _, route := range routes {
		operations := make(map[string]bool)
		if err := collectOperations(definitions[route.Request], bindingID, documents, make(map[string]bool), operations); err != nil {
			return result, fmt.Errorf("request %s operations: %w", route.Request, err)
		}
		if len(operations) == 0 {
			return result, fmt.Errorf("request %s has no declared operations", route.Request)
		}
		var missing []string
		for name := range operations {
			if !covered[route.Request][name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return result, fmt.Errorf("request %s missing positive operation cases: %s", route.Request, strings.Join(missing, ", "))
		}
		result.Operations += len(operations)
	}
	requestDefinitions := make(map[string]bool)
	for _, route := range routes {
		requestDefinitions[route.Request] = true
	}
	var mappings []operationMapping
	var errorMappings map[string]map[string]int
	if err := readTyped(filepath.Join(filepath.Dir(routesPath), "operations.json"), &mappings); err != nil {
		return result, err
	}
	if err := readTyped(filepath.Join(filepath.Dir(routesPath), "errors.json"), &errorMappings); err != nil {
		return result, err
	}
	if err := validateCatalogs(mappings, errorMappings, routes, compiled, documents); err != nil {
		return result, err
	}
	for _, scenario := range input.Scenarios {
		if err := uniqueID(seen, scenario.ID); err != nil {
			return result, err
		}
		if strings.TrimSpace(scenario.Scope) == "" || len(scenario.Steps) == 0 || len(scenario.Invariants) == 0 {
			return result, fmt.Errorf("scenario %s requires scope, steps and invariants", scenario.ID)
		}
		for _, invariant := range scenario.Invariants {
			if strings.TrimSpace(invariant) == "" {
				return result, fmt.Errorf("scenario %s contains an empty invariant", scenario.ID)
			}
		}
		for i, step := range scenario.Steps {
			if !requestDefinitions[step.Definition] {
				return result, fmt.Errorf("scenario %s step %d references non-route request %q", scenario.ID, i+1, step.Definition)
			}
			if err := compiled[step.Definition].Validate(step.Request); err != nil {
				return result, fmt.Errorf("scenario %s step %d request shape: %w", scenario.ID, i+1, err)
			}
			if step.Expected.HTTPStatus < 100 || step.Expected.HTTPStatus > 599 {
				return result, fmt.Errorf("scenario %s step %d has invalid expected HTTP status", scenario.ID, i+1)
			}
			if step.Expected.ErrorCode != "" || step.Expected.ErrorScope != "" {
				status, exists := errorMappings[step.Expected.ErrorScope][step.Expected.ErrorCode]
				if !exists || status != step.Expected.HTTPStatus {
					return result, fmt.Errorf("scenario %s step %d expected error scope/code/status differs from errors.json", scenario.ID, i+1)
				}
			} else if step.Expected.HTTPStatus != 200 {
				return result, fmt.Errorf("scenario %s step %d must specify error_scope and error_code for a non-success response", scenario.ID, i+1)
			}
			result.Steps++
		}
	}
	result.Scenarios = len(input.Scenarios)
	return result, nil
}

func validateCatalogs(mappings []operationMapping, errorMappings map[string]map[string]int, routes []route, compiled map[string]*jsonschema.Schema, docs map[string]map[string]any) error {
	definitions := docs[bindingID]["$defs"].(map[string]any)
	expected := make(map[string]bool)
	for _, route := range routes {
		operations := make(map[string]bool)
		if err := collectOperations(definitions[route.Request], bindingID, docs, make(map[string]bool), operations); err != nil {
			return err
		}
		for operation := range operations {
			expected[route.Path+" "+operation] = true
		}
	}
	seen := make(map[string]bool)
	for _, mapping := range mappings {
		key := mapping.Route + " " + mapping.Operation
		if !expected[key] || seen[key] || strings.TrimSpace(mapping.Authorization) == "" || compiled[mapping.Request] == nil || compiled[mapping.Response] == nil {
			return fmt.Errorf("invalid/duplicate operation mapping %q or missing definitions/authorization", key)
		}
		operations := make(map[string]bool)
		if err := collectOperations(definitions[mapping.Request], bindingID, docs, make(map[string]bool), operations); err != nil {
			return err
		}
		if !operations[mapping.Operation] {
			return fmt.Errorf("operation mapping %q request does not declare its operation", key)
		}
		seen[key] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("operations.json covers %d of %d declared operations", len(seen), len(expected))
	}
	if len(errorMappings) != 2 || errorMappings["binding"] == nil || errorMappings["core"] == nil {
		return errors.New("errors.json must contain exactly binding and core scopes")
	}
	for scope, mapping := range errorMappings {
		var list []any
		if scope == "binding" {
			list = enumAt(definitions["BindingError"], "properties", "code", "enum")
		} else {
			defs, _ := docs[coreID]["$defs"].(map[string]any)
			list = enumAt(defs["Error"], "properties", "kind", "enum")
		}
		if len(list) == 0 || len(list) != len(mapping) {
			return fmt.Errorf("%s errors catalog does not match schema enum", scope)
		}
		for _, item := range list {
			code, ok := item.(string)
			status, exists := mapping[code]
			if !ok || !exists || status < 400 || status > 599 {
				return fmt.Errorf("%s error %q is missing or has invalid HTTP status", scope, code)
			}
		}
	}
	return nil
}

func enumAt(value any, path ...string) []any {
	for _, key := range path {
		object, _ := value.(map[string]any)
		value = object[key]
	}
	list, _ := value.([]any)
	return list
}

func validateRoutes(routes []route, compiled map[string]*jsonschema.Schema) error {
	want := map[string][2]string{
		"/v1/commands": {"CommandRequest", "CommandResponse"},
		"/v1/actions":  {"ActionRequest", "ActionResponse"},
		"/v1/queries":  {"QueryRequest", "QueryResponse"},
	}
	if len(routes) != len(want) {
		return fmt.Errorf("expected exactly %d routes, got %d", len(want), len(routes))
	}
	seen := make(map[string]bool)
	for _, route := range routes {
		mapping, ok := want[route.Path]
		if !ok || seen[route.Path] || route.Method != "POST" || route.Request != mapping[0] || route.Response != mapping[1] {
			return fmt.Errorf("unexpected or duplicate route mapping: %+v", route)
		}
		if compiled[route.Request] == nil || compiled[route.Response] == nil {
			return fmt.Errorf("route %s references an unknown request/response definition", route.Path)
		}
		seen[route.Path] = true
	}
	return nil
}

func uniqueID(seen map[string]bool, id string) error {
	if strings.TrimSpace(id) == "" || seen[id] {
		return fmt.Errorf("empty or duplicate vector/scenario id %q", id)
	}
	seen[id] = true
	return nil
}

func operation(value any) string {
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if command, ok := obj["command"].(map[string]any); ok {
		obj = command
	}
	name, _ := obj["operation"].(string)
	return name
}

// Follow only schema references and structural branches. This is operation
// coverage discovery, not a second schema validator or a behavior interpreter.
func collectOperations(value any, base string, docs map[string]map[string]any, seen map[string]bool, found map[string]bool) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if ref, ok := obj["$ref"].(string); ok {
		id, fragment, _ := strings.Cut(ref, "#")
		if id == "" {
			id = base
		}
		key := id + "#" + fragment
		if !seen[key] {
			seen[key] = true
			var target any = docs[id]
			if target == nil {
				return fmt.Errorf("unregistered reference %q", ref)
			}
			if fragment != "" {
				if !strings.HasPrefix(fragment, "/") {
					return fmt.Errorf("operation discovery requires JSON pointer reference, got %q", ref)
				}
				for _, part := range strings.Split(fragment[1:], "/") {
					part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
					mapping, _ := target.(map[string]any)
					target = mapping[part]
				}
			}
			if target == nil {
				return fmt.Errorf("unresolved reference %q", ref)
			}
			if err := collectOperations(target, id, docs, seen, found); err != nil {
				return err
			}
		}
	}
	if properties, ok := obj["properties"].(map[string]any); ok {
		if op, ok := properties["operation"].(map[string]any); ok {
			if name, ok := op["const"].(string); ok {
				found[name] = true
			}
			if names, ok := op["enum"].([]any); ok {
				for _, name := range names {
					if text, ok := name.(string); ok {
						found[text] = true
					}
				}
			}
		}
		if command, ok := properties["command"]; ok {
			if err := collectOperations(command, base, docs, seen, found); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := obj[keyword].([]any); ok {
			for _, branch := range branches {
				if err := collectOperations(branch, base, docs, seen, found); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func pointerEscape(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "~", "~0"), "/", "~1")
}

func readDocument(path, wantID string) (map[string]any, error) {
	var doc map[string]any
	if err := readTyped(path, &doc); err != nil {
		return nil, err
	}
	if doc["$id"] != wantID {
		return nil, fmt.Errorf("%s: expected $id %s", path, wantID)
	}
	return doc, nil
}

func readTyped(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// This checks artifact ambiguity only. It is deliberately not the future
// service's protocol parser and makes no claim about production input limits.
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, isDelim := token.(json.Delim)
		if !isDelim {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid object key %q", key)
				}
				seen[name] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected delimiter %q", delim)
		}
		_, err = decoder.Token()
		return err
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}
