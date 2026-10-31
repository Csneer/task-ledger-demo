package contract

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
)

func parse(t *testing.T, raw string) any {
	t.Helper()
	v, err := jsonvalue.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEmbeddedSchemasAndAllStructureVectors(t *testing.T) {
	raw, err := os.ReadFile("../../contract/v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	vectors := parse(t, string(raw)).(map[string]any)["cases"].([]any)
	t.Chdir(t.TempDir()) // Validation must not depend on a checkout or cwd.
	for _, v := range vectors {
		c := v.(map[string]any)
		t.Run(c["id"].(string), func(t *testing.T) {
			err := ValidateBinding(c["definition"].(string), c["value"])
			if (err == nil) != c["valid"].(bool) {
				t.Fatalf("valid=%v error=%v", c["valid"], err)
			}
		})
	}
	if err := ValidateCore("ActorRef", parse(t, `{"kind":"human","id":"owner"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCore("Unknown", map[string]any{}); err == nil {
		t.Fatal("accepted unknown definition")
	}
}

func TestExtremeNumbersDoNotExpandOrAlterInput(t *testing.T) {
	for _, text := range []string{"1e-999999999", "-1e-999999999999999999999999", "1." + strings.Repeat("1", 100000) + "e-100000"} {
		value := parse(t, `{"org.example":{"small":`+text+`}}`)
		if err := ValidateCore("Extensions", value); err != nil {
			t.Fatal(err)
		}
		if value.(map[string]any)["org.example"].(map[string]any)["small"].(json.Number).String() != text {
			t.Fatal("validation changed original number")
		}
		command := parse(t, `{"protocol_version":"task-core-draft-1","command_id":"x","task":{"authority_id":"a","task_id":"t"},"operation":"task.create","expected_revision":`+text+`,"body":{"goal":"g","expected_result":"r"}}`)
		if err := ValidateCore("Command", command); err == nil {
			t.Fatal("accepted fractional revision")
		}
	}
	if err := ValidateCore("Command", parse(t, `{"protocol_version":"task-core-draft-1","command_id":"x","task":{"authority_id":"a","task_id":"t"},"operation":"task.create","expected_revision":0e999999999,"body":{"goal":"g","expected_result":"r"}}`)); err != nil {
		t.Fatal("zero exponent should be harmless:", err)
	}
}

func TestProjectionAuditRejectsFutureUnsafeSchemas(t *testing.T) {
	for _, schema := range []string{
		`{"minimum":0.1}`, `{"maximum":9007199254740992}`, `{"multipleOf":1}`,
		`{"enum":[0.1]}`, `{"const":{"nested":[0.1]}}`,
		`{"uniqueItems":true,"items":{"type":"number"}}`,
		`{"uniqueItems":true,"items":{"enum":["x",1]}}`,
	} {
		if err := auditNumericConstraints(parse(t, schema)); err == nil {
			t.Fatalf("accepted unsafe projection schema: %s", schema)
		}
	}
	for _, schema := range []string{
		`{"type":"integer","minimum":0,"maximum":9007199254740991}`,
		`{"uniqueItems":true,"items":{"enum":["x","y"]}}`,
	} {
		if err := auditNumericConstraints(parse(t, schema)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNumericProjectionKeepsIntegerValidationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		number string
		valid  bool
	}{
		{"0", true}, {"1.0", true}, {"1e0", true}, {"9007199254740991", true},
		{"9007199254740992", false}, {"-1", false}, {"0.999999999999999999", false},
		{"9007199254740991.00000001", false}, {"1e308", false},
	} {
		value := parse(t, `{"protocol_version":"task-core-draft-1","command_id":"x","task":{"authority_id":"a","task_id":"t"},"operation":"task.create","expected_revision":`+tc.number+`,"body":{"goal":"g","expected_result":"r"}}`)
		if err := ValidateCore("Command", value); (err == nil) != tc.valid {
			t.Fatalf("revision %s valid=%v error=%v", tc.number, tc.valid, err)
		}
	}
}
