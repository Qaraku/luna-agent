package packages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema 是包协议支持的有界 JSON Schema 子集；不支持的关键字在清单解析时拒绝。
type Schema struct {
	Type                 string             `json:"type"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties bool               `json:"additionalProperties"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []json.RawMessage  `json:"enum,omitempty"`
	MaxLength            int                `json:"maxLength,omitempty"`
	MaxItems             int                `json:"maxItems,omitempty"`
}

func (s *Schema) normalize(depth int) error {
	if s == nil || depth > 6 || len(s.Description) > 4096 {
		return fmt.Errorf("schema exceeds nesting/text limits")
	}
	if s.AdditionalProperties {
		return fmt.Errorf("tool parameter objects must reject unknown fields")
	}
	switch s.Type {
	case "object":
		if len(s.Properties) > 32 || s.Items != nil || s.MaxItems != 0 || s.MaxLength != 0 || len(s.Enum) > 0 {
			return fmt.Errorf("invalid object schema")
		}
		for name, child := range s.Properties {
			if !parameterName.MatchString(name) {
				return fmt.Errorf("invalid schema property name")
			}
			if err := child.normalize(depth + 1); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, name := range s.Required {
			if s.Properties[name] == nil || seen[name] {
				return fmt.Errorf("invalid required property")
			}
			seen[name] = true
		}
	case "array":
		if s.Items == nil || len(s.Properties) > 0 || len(s.Required) > 0 || len(s.Enum) > 0 || s.MaxLength != 0 {
			return fmt.Errorf("array schema requires bounded items")
		}
		if s.MaxItems == 0 {
			s.MaxItems = 64
		}
		if s.MaxItems < 1 || s.MaxItems > 128 {
			return fmt.Errorf("array maxItems must be 1..128")
		}
		return s.Items.normalize(depth + 1)
	case "string", "boolean", "number", "integer":
		if len(s.Properties) > 0 || len(s.Required) > 0 || s.Items != nil || s.MaxItems != 0 {
			return fmt.Errorf("invalid scalar schema")
		}
		if s.Type == "string" {
			if s.MaxLength == 0 {
				s.MaxLength = 16384
			}
			if s.MaxLength < 1 || s.MaxLength > 16384 {
				return fmt.Errorf("string maxLength must be 1..16384")
			}
		} else if s.MaxLength != 0 {
			return fmt.Errorf("maxLength applies to strings only")
		}
		if len(s.Enum) > 32 {
			return fmt.Errorf("enum limit exceeded")
		}
		for _, raw := range s.Enum {
			value, err := decodeValue(raw)
			if err != nil {
				return err
			}
			plain := *s
			plain.Enum = nil
			if err = plain.check(value, "enum"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported schema type %q", s.Type)
	}
	return nil
}
func decodeValue(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return value, nil
}
func (s Schema) ValidateJSON(raw json.RawMessage) error {
	if len(raw) > 64*1024 || !utf8.Valid(raw) {
		return fmt.Errorf("tool arguments exceed the JSON text limit")
	}
	value, err := decodeValue(raw)
	if err != nil {
		return fmt.Errorf("invalid tool arguments")
	}
	return s.check(value, "arguments")
}
func (s Schema) check(value any, where string) error {
	switch s.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", where)
		}
		for _, name := range s.Required {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("%s.%s is required", where, name)
			}
		}
		for name, item := range object {
			child, ok := s.Properties[name]
			if !ok {
				return fmt.Errorf("%s contains an unknown field", where)
			}
			if err := child.check(item, where+"."+name); err != nil {
				return err
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok || len(items) > s.MaxItems {
			return fmt.Errorf("%s exceeds its array constraint", where)
		}
		for _, item := range items {
			if err := s.Items.check(item, where+"[]"); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok || utf8.RuneCountInString(text) > s.MaxLength {
			return fmt.Errorf("%s exceeds its string constraint", where)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", where)
		}
	case "number", "integer":
		number, ok := value.(json.Number)
		if !ok || len(number) > 128 {
			return fmt.Errorf("%s must be a bounded number", where)
		}
		if at := strings.IndexAny(string(number), "eE"); at >= 0 {
			exponent, err := strconv.Atoi(string(number)[at+1:])
			if err != nil || exponent < -308 || exponent > 308 {
				return fmt.Errorf("%s numeric exponent exceeds supported range", where)
			}
		}
		f, err := strconv.ParseFloat(string(number), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("%s number is out of range", where)
		}
		if s.Type == "integer" {
			rat, ok := new(big.Rat).SetString(string(number))
			if !ok || !rat.IsInt() {
				return fmt.Errorf("%s must be an integer", where)
			}
		}
	default:
		return fmt.Errorf("unsupported schema")
	}
	if len(s.Enum) > 0 {
		found := false
		for _, raw := range s.Enum {
			candidate, _ := decodeValue(raw)
			if reflect.DeepEqual(candidate, value) {
				found = true
				break
			}
			a, aok := candidate.(json.Number)
			b, bok := value.(json.Number)
			if aok && bok {
				left, lok := new(big.Rat).SetString(string(a))
				right, rok := new(big.Rat).SetString(string(b))
				if lok && rok && left.Cmp(right) == 0 {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("%s is not an enum value", where)
		}
	}
	return nil
}
