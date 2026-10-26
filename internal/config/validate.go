package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"yunshu.dev/core/internal/jsonx"
)

// CodeConfigValueInvalid is the wire error code for every rejected write:
// unknown key, type mismatch, out-of-domain enum, out-of-range number, key
// outside the runtime whitelist.
const CodeConfigValueInvalid = "config_value_invalid"

// ErrConfigValueInvalid is the sentinel matched by errors.Is.
var ErrConfigValueInvalid = errors.New(CodeConfigValueInvalid)

// ValidationError carries the configuration key and the rejection reason.
type ValidationError struct {
	Key    Key
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", CodeConfigValueInvalid, e.Key, e.Reason)
}

// Unwrap allows errors.Is(err, ErrConfigValueInvalid).
func (e *ValidationError) Unwrap() error { return ErrConfigValueInvalid }

// Code returns the wire error code.
func (e *ValidationError) Code() string { return CodeConfigValueInvalid }

// HTTPStatus is the status the control plane must return for this error.
func (e *ValidationError) HTTPStatus() int { return 400 }

func invalid(key Key, format string, args ...any) *ValidationError {
	return &ValidationError{Key: key, Reason: fmt.Sprintf(format, args...)}
}

// DecimalScale is the fixed-point scale for TDS "decimal" configuration
// values. Four fractional digits cover 1.2, 0.5 and 1.00 without float
// arithmetic anywhere in the read path.
const DecimalScale = 10000

// Decimal is a fixed-point number with four fractional digits.
type Decimal int64

// DecimalZero is 0.0000.
const DecimalZero Decimal = 0

// ParseDecimal parses a plain decimal literal (no exponent) into fixed point.
// More than four fractional digits are rejected rather than rounded: config
// values must round-trip exactly.
func ParseDecimal(s string) (Decimal, error) {
	s = strings.TrimSpace(s)
	if err := validNumberLiteral(s); err != nil {
		return 0, err
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	wholePart, fracPart, _ := strings.Cut(s, ".")
	if len(fracPart) > 4 {
		return 0, fmt.Errorf("decimal %q has more than 4 fractional digits", s)
	}
	fracPart += strings.Repeat("0", 4-len(fracPart))

	whole, err := strconv.ParseInt(wholePart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decimal %q: %w", s, err)
	}
	frac, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decimal %q: %w", s, err)
	}
	if whole > (math.MaxInt64-frac)/DecimalScale {
		return 0, fmt.Errorf("decimal %q overflows", s)
	}
	v := Decimal(whole*DecimalScale + frac)
	if neg {
		v = -v
	}
	return v, nil
}

// String renders the decimal without trailing fractional zeros.
func (d Decimal) String() string {
	v := int64(d)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	whole := v / DecimalScale
	frac := v % DecimalScale
	if frac == 0 {
		return sign + strconv.FormatInt(whole, 10)
	}
	s := fmt.Sprintf("%04d", frac)
	s = strings.TrimRight(s, "0")
	return sign + strconv.FormatInt(whole, 10) + "." + s
}

// Float64 converts to float64 for display only. Do not use it in billing.
func (d Decimal) Float64() float64 { return float64(d) / DecimalScale }

// BasisPoints returns the value in basis points (×10000). It is the same
// integer for a decimal at scale 4, named for call sites that think in bp.
func (d Decimal) BasisPoints() int64 { return int64(d) }

// Value is one decoded configuration value. Its fields are unexported so a
// published value cannot be mutated through a getter.
type Value struct {
	kind ValueType
	raw  jsonx.RawMessage

	intVal  int64
	boolVal bool
	strVal  string
	decVal  Decimal
}

// Kind returns the declared value type.
func (v Value) Kind() ValueType { return v.kind }

// Raw returns the canonical JSON literal. The slice is shared with the
// immutable snapshot; callers must not modify it.
func (v Value) Raw() jsonx.RawMessage { return v.raw }

// Int returns the integer form when the value is an int.
func (v Value) Int() (int64, bool) {
	if v.kind != TypeInt {
		return 0, false
	}
	return v.intVal, true
}

// Bool returns the boolean form when the value is a bool.
func (v Value) Bool() (bool, bool) {
	if v.kind != TypeBool {
		return false, false
	}
	return v.boolVal, true
}

// String returns the string form when the value is an enum or string.
func (v Value) String() (string, bool) {
	if v.kind != TypeEnum && v.kind != TypeString {
		return "", false
	}
	return v.strVal, true
}

// Decimal returns the fixed-point form when the value is a decimal.
func (v Value) Decimal() (Decimal, bool) {
	if v.kind != TypeDecimal {
		return 0, false
	}
	return v.decVal, true
}

// Parse decodes and validates one raw JSON value against the key metadata.
func Parse(key Key, raw jsonx.RawMessage) (Value, error) {
	m, ok := metaByKey[key]
	if !ok {
		return Value{}, invalid(key, "unknown config key")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Value{}, invalid(key, "empty value")
	}
	switch m.Type {
	case TypeInt:
		return parseIntValue(key, trimmed, m.Int)
	case TypeBool:
		return parseBoolValue(key, trimmed)
	case TypeEnum:
		return parseStringValue(key, trimmed, m)
	case TypeString:
		return parseStringValue(key, trimmed, m)
	case TypeDecimal:
		return parseDecimalValue(key, trimmed, m)
	case TypeJSON:
		return parseJSONValue(key, trimmed, m)
	default:
		return Value{}, invalid(key, "unsupported value type %q", m.Type)
	}
}

// Validate reports whether raw is acceptable for key.
func Validate(key Key, raw jsonx.RawMessage) error {
	_, err := Parse(key, raw)
	return err
}

// ValidateRuntime applies the runtime-write gate: the key must be on the
// 12-key whitelist, the value must pass type validation, quota keys must use
// a V1-implemented value, and strategy names must be registered.
func ValidateRuntime(key Key, raw jsonx.RawMessage) error {
	_, err := validateRuntime(key, raw)
	return err
}

func validateRuntime(key Key, raw jsonx.RawMessage) (Value, error) {
	m, ok := metaByKey[key]
	if !ok {
		return Value{}, invalid(key, "unknown config key")
	}
	if !m.RuntimeWritable {
		return Value{}, invalid(key, "key is not runtime writable")
	}
	v, err := Parse(key, raw)
	if err != nil {
		return Value{}, err
	}
	if len(m.RuntimeEnum) > 0 {
		got, _ := v.String()
		if !contains(m.RuntimeEnum, got) {
			return Value{}, invalid(key, "runtime value %q is not implemented in V1.0; allowed: %s",
				got, strings.Join(m.RuntimeEnum, ", "))
		}
	}
	if m.Strategy != "" {
		got, _ := v.String()
		if _, ok := Resolve(m.Strategy, got); !ok {
			available := RegisteredNames(m.Strategy)
			if len(available) == 0 {
				available = []string{"<none registered>"}
			}
			return Value{}, invalid(key, "strategy %q is not registered for %s; available: %s",
				got, m.Strategy, strings.Join(available, ", "))
		}
	}
	return v, nil
}

func parseIntValue(key Key, raw jsonx.RawMessage, bounds *IntRange) (Value, error) {
	s := string(raw)
	if err := validNumberLiteral(s); err != nil {
		return Value{}, invalid(key, "expected integer, got %s", short(raw))
	}
	if strings.ContainsAny(s, ".eE") {
		return Value{}, invalid(key, "expected integer, got %s", short(raw))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return Value{}, invalid(key, "integer %s is out of range", short(raw))
	}
	if bounds != nil && (n < bounds.Min || n > bounds.Max) {
		return Value{}, invalid(key, "integer %d outside [%d, %d]", n, bounds.Min, bounds.Max)
	}
	return Value{kind: TypeInt, raw: jsonx.RawMessage(strconv.FormatInt(n, 10)), intVal: n}, nil
}

func parseBoolValue(key Key, raw jsonx.RawMessage) (Value, error) {
	var b bool
	if err := jsonx.Unmarshal(raw, &b); err != nil {
		return Value{}, invalid(key, "expected boolean, got %s", short(raw))
	}
	if b {
		return Value{kind: TypeBool, raw: jsonx.RawMessage("true"), boolVal: true}, nil
	}
	return Value{kind: TypeBool, raw: jsonx.RawMessage("false")}, nil
}

func parseStringValue(key Key, raw jsonx.RawMessage, m *KeyMeta) (Value, error) {
	var s string
	if err := jsonx.Unmarshal(raw, &s); err != nil {
		return Value{}, invalid(key, "expected string, got %s", short(raw))
	}
	if m.MaxLen > 0 && len(s) > m.MaxLen {
		return Value{}, invalid(key, "string length %d exceeds %d", len(s), m.MaxLen)
	}
	if len(m.Enum) > 0 && !contains(m.Enum, s) {
		return Value{}, invalid(key, "value %q outside domain: %s", s, strings.Join(m.Enum, ", "))
	}
	encoded, err := jsonx.Marshal(s)
	if err != nil {
		return Value{}, invalid(key, "cannot encode string")
	}
	return Value{kind: m.Type, raw: encoded, strVal: s}, nil
}

func parseDecimalValue(key Key, raw jsonx.RawMessage, m *KeyMeta) (Value, error) {
	d, err := ParseDecimal(string(raw))
	if err != nil {
		return Value{}, invalid(key, "expected decimal, got %s", short(raw))
	}
	if m.Dec != nil && (d < m.Dec.Min || d > m.Dec.Max) {
		return Value{}, invalid(key, "decimal %s outside [%s, %s]", d, m.Dec.Min, m.Dec.Max)
	}
	return Value{kind: TypeDecimal, raw: jsonx.RawMessage(strings.TrimSpace(string(raw))), decVal: d}, nil
}

func parseJSONValue(key Key, raw jsonx.RawMessage, m *KeyMeta) (Value, error) {
	var obj map[string]jsonx.RawMessage
	if err := jsonx.Unmarshal(raw, &obj); err != nil || obj == nil {
		return Value{}, invalid(key, "expected JSON object, got %s", short(raw))
	}

	expected := make(map[string]FieldSpec, len(m.Fields))
	for _, f := range m.Fields {
		expected[f.Name] = f
	}
	for name, fieldRaw := range obj {
		spec, ok := expected[name]
		if !ok {
			return Value{}, invalid(key, "unknown object field %q", name)
		}
		if err := validateField(key, spec, fieldRaw); err != nil {
			return Value{}, err
		}
	}
	for _, spec := range m.Fields {
		if _, ok := obj[spec.Name]; !ok {
			return Value{}, invalid(key, "missing object field %q", spec.Name)
		}
	}

	canonical, err := jsonx.Marshal(obj)
	if err != nil {
		return Value{}, invalid(key, "cannot encode JSON object")
	}
	return Value{kind: TypeJSON, raw: canonical}, nil
}

func validateField(key Key, spec FieldSpec, raw jsonx.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	switch spec.Type {
	case TypeInt:
		if err := validNumberLiteral(string(trimmed)); err != nil || strings.ContainsAny(string(trimmed), ".eE") {
			return invalid(key, "field %q must be an integer", spec.Name)
		}
		n, err := strconv.ParseInt(string(trimmed), 10, 64)
		if err != nil {
			return invalid(key, "field %q is out of range", spec.Name)
		}
		if n < spec.Int.Min || n > spec.Int.Max {
			return invalid(key, "field %q outside [%d, %d]", spec.Name, spec.Int.Min, spec.Int.Max)
		}
	case TypeDecimal:
		d, err := ParseDecimal(string(trimmed))
		if err != nil {
			return invalid(key, "field %q must be a decimal", spec.Name)
		}
		if d < spec.Dec.Min || d > spec.Dec.Max {
			return invalid(key, "field %q outside [%s, %s]", spec.Name, spec.Dec.Min, spec.Dec.Max)
		}
	default:
		return invalid(key, "field %q has unsupported type %q", spec.Name, spec.Type)
	}
	return nil
}

// validNumberLiteral enforces plain JSON number syntax without an exponent.
// Configuration values are stored as typed JSON; accepting 1e3 would make the
// int/decimal distinction dependent on the writer.
func validNumberLiteral(s string) error {
	if s == "" {
		return errors.New("empty number")
	}
	i := 0
	if s[0] == '-' {
		i++
		if i == len(s) {
			return errors.New("bare minus")
		}
	}
	if s[i] == '0' {
		i++
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			return errors.New("leading zero")
		}
	} else if s[i] >= '1' && s[i] <= '9' {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	} else {
		return errors.New("not a number")
	}
	if i < len(s) && s[i] == '.' {
		i++
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return errors.New("empty fraction")
		}
	}
	if i != len(s) {
		return errors.New("trailing characters")
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func short(raw []byte) string {
	const limit = 64
	s := strings.TrimSpace(string(raw))
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
