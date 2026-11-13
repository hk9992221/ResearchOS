package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Arguments is a decoded tool argument object with typed accessors. Bindings
// run after the host validated the schema, so a type mismatch here is a
// programming error surfaced as INVALID_ARGUMENT rather than a panic.
type Arguments struct {
	raw json.RawMessage
	Obj map[string]any
}

// DecodeArguments parses a raw argument payload. Empty input is an empty
// object.
func DecodeArguments(raw json.RawMessage) (*Arguments, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, Errorf(CodeInvalidArgument, "参数必须是 JSON 对象").Wrap(err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return &Arguments{raw: trimmed, Obj: obj}, nil
}

// Raw returns the original bytes.
func (a *Arguments) Raw() json.RawMessage { return a.raw }

// Has reports whether name was supplied.
func (a *Arguments) Has(name string) bool {
	_, ok := a.Obj[name]
	return ok
}

// String returns a string value and whether it was present.
func (a *Arguments) String(name string) (string, bool) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
}

// RequiredString returns name or an INVALID_ARGUMENT error.
func (a *Arguments) RequiredString(name string) (string, error) {
	s, ok := a.String(name)
	if !ok || s == "" {
		return "", Errorf(CodeInvalidArgument, "缺少必填参数 %s", name)
	}
	return s, nil
}

// Int returns an integer value; JSON numbers and numeric strings are accepted.
func (a *Arguments) Int(name string) (int, bool) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n, true
		}
	case float64:
		return int(v), true
	}
	return 0, false
}

// Bool returns a boolean value.
func (a *Arguments) Bool(name string) (bool, bool) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return false, false
	}
	b, ok := raw.(bool)
	return b, ok
}

// BoolDefault returns the boolean or def when absent.
func (a *Arguments) BoolDefault(name string, def bool) bool {
	if b, ok := a.Bool(name); ok {
		return b
	}
	return def
}

// Strings returns a string array.
func (a *Arguments) Strings(name string) ([]string, bool, error) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, Errorf(CodeInvalidArgument, "参数 %s 必须是字符串数组", name)
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, true, Errorf(CodeInvalidArgument, "参数 %s[%d] 必须是字符串", name, i)
		}
		out = append(out, s)
	}
	return out, true, nil
}

// Ints returns an integer array.
func (a *Arguments) Ints(name string) ([]int, bool, error) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, Errorf(CodeInvalidArgument, "参数 %s 必须是整数数组", name)
	}
	out := make([]int, 0, len(list))
	for i, item := range list {
		num, ok := item.(json.Number)
		if !ok {
			return nil, true, Errorf(CodeInvalidArgument, "参数 %s[%d] 必须是整数", name, i)
		}
		n, err := num.Int64()
		if err != nil {
			return nil, true, Errorf(CodeInvalidArgument, "参数 %s[%d] 必须是整数", name, i)
		}
		out = append(out, int(n))
	}
	return out, true, nil
}

// Object returns a nested object.
func (a *Arguments) Object(name string) (map[string]any, bool) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return nil, false
	}
	obj, ok := raw.(map[string]any)
	return obj, ok
}

// Objects returns an array of objects.
func (a *Arguments) Objects(name string) ([]map[string]any, bool, error) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, Errorf(CodeInvalidArgument, "参数 %s 必须是对象数组", name)
	}
	out := make([]map[string]any, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, true, Errorf(CodeInvalidArgument, "参数 %s[%d] 必须是对象", name, i)
		}
		out = append(out, obj)
	}
	return out, true, nil
}

// RawField returns one field as raw JSON.
func (a *Arguments) RawField(name string) (json.RawMessage, bool, error) {
	raw, ok := a.Obj[name]
	if !ok {
		return nil, false, nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, true, Errorf(CodeInvalidArgument, "参数 %s 无法序列化", name)
	}
	return body, true, nil
}

// Reencode marshals obj deterministically (encoding/json sorts map keys).
func Reencode(obj map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode arguments: %w", err)
	}
	return body, nil
}
