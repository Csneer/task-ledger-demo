package jsonvalue

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func mustParse(t *testing.T, raw string) any {
	t.Helper()
	v, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRejectAmbiguousOrInvalidJSON(t *testing.T) {
	for _, raw := range []string{
		`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"nested":{"a":1,"a":2}}`,
		`"\ud800"`, `"\udc00"`, `"\ud800\u0041"`, `"\ud800\ud800"`,
		`"\ud800x"`, `"\u00xy"`, string([]byte{'"', 0xff, '"'}),
		`NaN`, `Infinity`, `-Infinity`, `1e309`, `-1.7976931348623158e308`,
		`1.79769313486231570000000000000001e308`, `1e999999999`,
		`1 2`, `{}[]`, `[1,]`, `{"x":}`, `01`, `+1`, ``,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("accepted invalid input %q", raw)
			}
		})
	}
}

func TestUnicodeAndExactNumberRoundTrip(t *testing.T) {
	raw := `{"text":"人😀\ud83d\ude00\\ud800","fraction":0.123456789012345678901234567890,"bound":1.7976931348623157e308}`
	v := mustParse(t, raw).(map[string]any)
	if v["text"] != "人😀😀\\ud800" {
		t.Fatalf("wrong Unicode: %q", v["text"])
	}
	if v["fraction"].(json.Number).String() != "0.123456789012345678901234567890" {
		t.Fatal("number was rounded")
	}
	encoded, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(v, mustParse(t, string(encoded))) {
		t.Fatal("round-trip changed exact value")
	}
}

func TestExactEquality(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{`1`, `1.0`, true}, {`10e-1`, `1000e-3`, true}, {`-0e999999999`, `0`, true},
		{`1e-999999999`, `10e-1000000000`, true},
		{`1e-999999999`, `2e-999999999`, false},
		{`0.123456789012345678901`, `0.123456789012345678902`, false},
		{`true`, `1`, false}, {`false`, `0`, false}, {`{}`, `{"x":null}`, false},
		{`{"a":1,"b":[2,3]}`, `{"b":[2.0,3],"a":1e0}`, true},
		{`[1,2]`, `[2,1]`, false}, {`"é"`, `"e\u0301"`, false},
	} {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			if got := Equal(mustParse(t, tc.a), mustParse(t, tc.b)); got != tc.equal {
				t.Fatalf("got %v, want %v", got, tc.equal)
			}
		})
	}
}

func TestIntegerAndExponentResources(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int64
	}{
		{"1.0", 1}, {"1e0", 1}, {"100e-2", 1}, {"-2.000", -2},
		{"9007199254740991", MaxInteger}, {"-9007199254740991", -MaxInteger},
		{"0e999999999999999999999999999999999", 0},
	} {
		got, err := Integer(json.Number(tc.text))
		if err != nil || got != tc.want {
			t.Fatalf("Integer(%s) = %d, %v", tc.text, got, err)
		}
	}
	for _, value := range []any{true, nil, "1", json.Number("1.5"), json.Number("1e-999999999"), json.Number("9007199254740992")} {
		if _, err := Integer(value); err == nil {
			t.Fatalf("accepted non-safe integer %v", value)
		}
	}
	for _, raw := range []string{"1e-999999999999999999999999999999", "0e999999999999999999999999999999", "1." + strings.Repeat("1", 100000) + "e-100000"} {
		value := mustParse(t, raw).(json.Number)
		projected, err := IntegerSchemaNumber(value)
		if err != nil || len(projected.String()) > 20 {
			t.Fatalf("unbounded projection: %q %v", projected, err)
		}
	}
}

func TestIntegerSchemaNumberIntervals(t *testing.T) {
	for _, tc := range []struct{ input, output string }{
		{"0e999999999", "0"}, {"1.000", "1"}, {"1e308", "9007199254740992"},
		{"-1e308", "-9007199254740992"}, {"1e-999999999", "0.5"},
		{"-1e-999999999", "-0.5"}, {"1.0000000000000000001", "1.5"},
		{"9007199254740991.0000001", "9007199254740991.5"},
		{"9007199254740992.1", "9007199254740992.5"},
		{"123456789012345678901.0001", "9007199254740992.5"},
	} {
		got, err := IntegerSchemaNumber(json.Number(tc.input))
		if err != nil || got.String() != tc.output {
			t.Fatalf("project %s = %s, %v; want %s", tc.input, got, err, tc.output)
		}
	}
}

func TestMarshalRejectsRepairAndCloneDetaches(t *testing.T) {
	for _, value := range []any{string([]byte{0xff}), map[string]any{string([]byte{0xff}): 1}, json.Number("1e309"), math.Inf(1)} {
		if _, err := Marshal(value); err == nil {
			t.Fatalf("Marshal accepted invalid value %v", value)
		}
	}
	original := mustParse(t, `{"nested":{"items":[1,2]}}`).(map[string]any)
	copy, err := Clone(original)
	if err != nil {
		t.Fatal(err)
	}
	copy.(map[string]any)["nested"].(map[string]any)["items"].([]any)[0] = 9
	if !Equal(original, mustParse(t, `{"nested":{"items":[1,2]}}`)) {
		t.Fatal("clone aliased original")
	}
}

func TestDepthBoundAndAcceptedDepthRoundTrip(t *testing.T) {
	if _, err := Parse([]byte(strings.Repeat("[", maxDepth+2) + "0" + strings.Repeat("]", maxDepth+2))); err == nil {
		t.Fatal("accepted excessive nesting")
	}
	v := mustParse(t, strings.Repeat("[", 200)+"0"+strings.Repeat("]", 200))
	if _, err := Marshal(v); err != nil {
		t.Fatal("valid parsed depth cannot round trip:", err)
	}
}
