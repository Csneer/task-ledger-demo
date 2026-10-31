// Package contract validates the two pinned schemas offline. This is not a
// generic JSON Schema adapter: its numeric projection is valid only for schemas
// containing safe-integer numeric constraints, verified at initialization.
package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	binding "github.com/Csneer/task-ledger-demo/contract/v1"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/protocol/upstream"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const coreID = "urn:agent-task-bridge:task-core-draft-1"
const bindingID = "urn:task-ledger:ledger-http-draft-1"

var once sync.Once
var initializationError error
var validators map[string]map[string]*jsonschema.Schema

type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema resource %q is not embedded; network loading disabled", url)
}

func initialize() {
	validators = map[string]map[string]*jsonschema.Schema{}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(offlineLoader{})
	documents := map[string]map[string]any{}
	for id, raw := range map[string][]byte{coreID: upstream.Schema, bindingID: binding.Schema} {
		value, err := jsonvalue.Parse(raw)
		if err != nil {
			initializationError = err
			return
		}
		doc, ok := value.(map[string]any)
		if !ok || doc["$id"] != id {
			initializationError = errors.New("embedded schema identity mismatch")
			return
		}
		if err := auditNumericConstraints(doc); err != nil {
			initializationError = fmt.Errorf("schema %s is incompatible with bounded numeric validation: %w", id, err)
			return
		}
		if err := compiler.AddResource(id, doc); err != nil {
			initializationError = err
			return
		}
		documents[id] = doc
	}
	for id, doc := range documents {
		definitions, ok := doc["$defs"].(map[string]any)
		if !ok {
			initializationError = errors.New("embedded schema has no definitions")
			return
		}
		validators[id] = map[string]*jsonschema.Schema{}
		for name := range definitions {
			schema, err := compiler.Compile(id + "#/$defs/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1"))
			if err != nil {
				initializationError = err
				return
			}
			validators[id][name] = schema
		}
	}
}

// auditNumericConstraints makes the projection's assumptions executable. Every
// schema numeric literal is a safe integer. MultipleOf is not supported; unique
// arrays must explicitly restrict items to strings. This catches future schema
// changes instead of silently applying the projection to new decimal rules.
func auditNumericConstraints(value any) error {
	switch v := value.(type) {
	case json.Number:
		if _, err := jsonvalue.Integer(v); err != nil {
			return errors.New("numeric schema literal is not a safe integer")
		}
	case []any:
		for _, child := range v {
			if err := auditNumericConstraints(child); err != nil {
				return err
			}
		}
	case map[string]any:
		if _, exists := v["multipleOf"]; exists {
			return errors.New("multipleOf requires general exact decimal validation")
		}
		if unique, _ := v["uniqueItems"].(bool); unique && !stringItems(v["items"]) {
			return errors.New("uniqueItems without exclusively string items is unsupported")
		}
		for _, child := range v {
			if err := auditNumericConstraints(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func stringItems(value any) bool {
	item, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if item["type"] == "string" {
		return true
	}
	if values, ok := item["enum"].([]any); ok && len(values) > 0 {
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}

func project(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		return jsonvalue.IntegerSchemaNumber(v)
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			x, err := project(child)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			x, err := project(child)
			if err != nil {
				return nil, err
			}
			out[key] = x
		}
		return out, nil
	default:
		return value, nil
	}
}

func validate(id, definition string, value any) error {
	once.Do(initialize)
	if initializationError != nil {
		return initializationError
	}
	schema, ok := validators[id][definition]
	if !ok {
		return fmt.Errorf("unknown schema definition %q", definition)
	}
	// This both detaches caller state and checks raw Go strings/numbers before
	// projection. No caller object, original request or receipt is rewritten.
	value, err := jsonvalue.Clone(value)
	if err != nil {
		return err
	}
	value, err = project(value)
	if err != nil {
		return err
	}
	return schema.Validate(value)
}

func ValidateCore(definition string, value any) error {
	return validate(coreID, definition, value)
}

func ValidateBinding(definition string, value any) error {
	return validate(bindingID, definition, value)
}
