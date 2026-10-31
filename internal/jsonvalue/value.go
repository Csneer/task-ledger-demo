// Package jsonvalue preserves the exact JSON values used for protocol equality.
// Parse rejects encodings that encoding/json otherwise silently repairs.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxInteger int64 = 9007199254740991
const maxDepth = 256

var numberSyntax = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// Parse returns objects, arrays, strings, booleans, nil and json.Number values.
// It never expands a decimal exponent into a large numerator or denominator.
func Parse(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	if err := checkEscapes(raw); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON input")
	}
	return v, nil
}

func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("JSON nesting exceeds resource limit")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, exists := m[k]; exists {
					return nil, errors.New("duplicate object key")
				}
				v, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, errors.New("invalid object closing delimiter")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, errors.New("invalid array closing delimiter")
			}
			return a, nil
		default:
			return nil, errors.New("unexpected closing delimiter")
		}
	case json.Number:
		if _, err := normalize(t.String()); err != nil {
			return nil, err
		}
		return t, nil
	default:
		return token, nil
	}
}

// checkEscapes validates Unicode escapes before encoding/json can replace an
// unpaired surrogate with U+FFFD. The decoder validates other JSON syntax.
func checkEscapes(raw []byte) error {
	inside := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inside = !inside
			continue
		}
		if !inside || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return errors.New("unterminated escape")
		}
		if raw[i] != 'u' {
			continue
		}
		u, err := hex4(raw, i+1)
		if err != nil {
			return err
		}
		i += 4
		if u >= 0xdc00 && u <= 0xdfff {
			return errors.New("unpaired low surrogate")
		}
		if u >= 0xd800 && u <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return errors.New("unpaired high surrogate")
			}
			low, err := hex4(raw, i+3)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("invalid surrogate pair")
			}
			i += 6
		}
	}
	return nil
}

func hex4(raw []byte, start int) (uint64, error) {
	if start+4 > len(raw) {
		return 0, errors.New("short Unicode escape")
	}
	v, err := strconv.ParseUint(string(raw[start:start+4]), 16, 16)
	if err != nil {
		return 0, errors.New("invalid Unicode escape")
	}
	return v, nil
}

type decimal struct {
	negative bool
	digits   string
	exponent *big.Int // value = sign * digits * 10^exponent
}

func normalize(text string) (decimal, error) {
	var n decimal
	if !numberSyntax.MatchString(text) {
		return n, errors.New("invalid JSON number")
	}
	n.exponent = new(big.Int)
	if text[0] == '-' {
		n.negative = true
		text = text[1:]
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		if _, ok := n.exponent.SetString(text[i+1:], 10); !ok {
			return n, errors.New("invalid exponent")
		}
		text = text[:i]
	}
	if i := strings.IndexByte(text, '.'); i >= 0 {
		n.exponent.Sub(n.exponent, big.NewInt(int64(len(text)-i-1)))
		text = text[:i] + text[i+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		n.negative, n.digits = false, "0"
		n.exponent.SetInt64(0)
		return n, nil
	}
	n.digits = strings.TrimRight(text, "0")
	n.exponent.Add(n.exponent, big.NewInt(int64(len(text)-len(n.digits))))
	order := new(big.Int).Add(n.exponent, big.NewInt(int64(len(n.digits)-1)))
	if c := order.Cmp(big.NewInt(308)); c > 0 || c == 0 && compareDigits(n.digits, "17976931348623157") > 0 {
		return n, errors.New("number exceeds protocol magnitude")
	}
	return n, nil
}

// compareDigits compares significands padded with zeroes without allocating
// the padding, including exact values immediately above the binary64 bound.
func compareDigits(a, b string) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := byte('0'), byte('0')
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func number(value any) (decimal, bool) {
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		text = fmt.Sprint(v)
	default:
		return decimal{}, false
	}
	n, err := normalize(text)
	return n, err == nil
}

// Equal compares JSON values, not Go's coercions: true differs from 1, while
// exact numeric spellings 1, 1.0 and 1e0 compare equal. Parse raw input first.
func Equal(a, b any) bool {
	if na, ok := number(a); ok {
		nb, ok := number(b)
		return ok && na.negative == nb.negative && na.digits == nb.digits && na.exponent.Cmp(nb.exponent) == 0
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool:
		v, ok := b.(bool)
		return ok && a == v
	case string:
		v, ok := b.(string)
		return ok && a == v
	case []any:
		v, ok := b.([]any)
		if !ok || len(a) != len(v) {
			return false
		}
		for i := range a {
			if !Equal(a[i], v[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		v, ok := b.(map[string]any)
		if !ok || len(a) != len(v) {
			return false
		}
		for k, value := range a {
			other, exists := v[k]
			if !exists || !Equal(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Integer accepts mathematical integers, including 1.0, within the protocol's
// safe integer range. It does not convert fractional values or booleans.
func Integer(value any) (int64, error) {
	n, ok := number(value)
	if !ok || n.exponent.Sign() < 0 || !n.exponent.IsInt64() {
		return 0, errors.New("not a safe integer")
	}
	if n.digits == "0" {
		return 0, nil
	}
	exp := n.exponent.Int64()
	if exp > 16 || int64(len(n.digits))+exp > 16 {
		return 0, errors.New("integer exceeds safe range")
	}
	text := n.digits + strings.Repeat("0", int(exp))
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil || v > MaxInteger {
		return 0, errors.New("integer exceeds safe range")
	}
	if n.negative {
		v = -v
	}
	return v, nil
}

// IntegerSchemaNumber is a bounded validation-only projection. It preserves
// integer type and comparisons/equality against safe integer constants, but NOT
// general decimal equality. It must only be used with schemas audited to contain
// safe-integer numeric constraints and no number-sensitive uniqueness or
// multipleOf rules. The original parsed value remains unchanged.
func IntegerSchemaNumber(value json.Number) (json.Number, error) {
	n, err := normalize(value.String())
	if err != nil {
		return "", err
	}
	if n.digits == "0" {
		return json.Number("0"), nil
	}
	sign := ""
	if n.negative {
		sign = "-"
	}
	if n.exponent.Sign() >= 0 {
		if v, err := Integer(value); err == nil {
			return json.Number(strconv.FormatInt(v, 10)), nil
		}
		return json.Number(sign + "9007199254740992"), nil
	}
	point := new(big.Int).Add(n.exponent, big.NewInt(int64(len(n.digits))))
	if point.Sign() <= 0 {
		return json.Number(sign + "0.5"), nil
	}
	if !point.IsInt64() || point.Int64() > 16 {
		return json.Number(sign + "9007199254740992.5"), nil
	}
	whole := n.digits[:int(point.Int64())]
	if v, err := strconv.ParseInt(whole, 10, 64); err != nil || v > MaxInteger {
		whole = "9007199254740992"
	}
	return json.Number(sign + whole + ".5"), nil
}

// Marshal validates strings before encoding/json can repair invalid UTF-8 and
// then checks the encoded JSON. Already-rounded caller floats cannot recover
// their original decimal input; protocol inputs must enter through Parse.
func Marshal(value any) ([]byte, error) {
	if err := checkStrings(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err := Parse(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func checkStrings(v reflect.Value, depth int) error {
	if depth > maxDepth {
		return errors.New("value nesting exceeds resource limit")
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return errors.New("invalid UTF-8 string")
		}
	case reflect.Interface:
		if !v.IsNil() {
			return checkStrings(v.Elem(), depth)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			return checkStrings(v.Elem(), depth+1)
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if err := checkStrings(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if err := checkStrings(it.Key(), depth+1); err != nil {
				return err
			}
			if err := checkStrings(it.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.IsExported() && field.Tag.Get("json") != "-" {
				if err := checkStrings(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Clone creates a detached JSON tree with exact json.Number leaves.
func Clone(value any) (any, error) {
	raw, err := Marshal(value)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}
