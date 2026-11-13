package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Schema is a compiled subset of JSON Schema 2020-12 covering every keyword the
// tool catalog uses: type, required, properties, additionalProperties=false,
// enum, minLength/maxLength, minimum/maximum, items, minItems/maxItems and
// pattern. No external dependency is involved (SPEC-11 §5.1, T11-08).
type Schema struct {
	raw  json.RawMessage
	root *schemaNode
}

type schemaNode struct {
	types                []string
	properties           map[string]*schemaNode
	required             []string
	additionalAllowed    *bool // nil = unspecified (allowed), false = closed
	enum                 []json.RawMessage
	minLength, maxLength *int
	minimum, maximum     *float64
	items                *schemaNode
	minItems, maxItems   *int
	pattern              *string
}

// ParseSchema compiles raw. It rejects malformed schemas and unsupported
// shapes so a bad tool definition fails at registration, not at call time.
func ParseSchema(raw json.RawMessage) (*Schema, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("schema: empty")
	}
	var generic map[string]any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	node, err := compileNode(generic, "$")
	if err != nil {
		return nil, err
	}
	return &Schema{raw: trimmed, root: node}, nil
}

func compileNode(v any, path string) (*schemaNode, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema %s: expected an object", path)
	}
	n := &schemaNode{}
	if raw, ok := obj["type"]; ok {
		switch t := raw.(type) {
		case string:
			n.types = []string{t}
		case []any:
			for _, item := range t {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("schema %s: type entries must be strings", path)
				}
				n.types = append(n.types, s)
			}
		default:
			return nil, fmt.Errorf("schema %s: invalid type", path)
		}
	}
	if raw, ok := obj["required"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("schema %s: required must be an array", path)
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("schema %s: required entries must be strings", path)
			}
			n.required = append(n.required, s)
		}
	}
	if raw, ok := obj["properties"]; ok {
		props, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %s: properties must be an object", path)
		}
		n.properties = make(map[string]*schemaNode, len(props))
		for name, child := range props {
			node, err := compileNode(child, path+"."+name)
			if err != nil {
				return nil, err
			}
			n.properties[name] = node
		}
	}
	if raw, ok := obj["additionalProperties"]; ok {
		allowed, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("schema %s: additionalProperties must be a boolean", path)
		}
		n.additionalAllowed = &allowed
	}
	if raw, ok := obj["enum"]; ok {
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("schema %s: enum must be a non-empty array", path)
		}
		for _, item := range list {
			body, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("schema %s: enum: %w", path, err)
			}
			n.enum = append(n.enum, body)
		}
	}
	var err error
	if n.minLength, err = intKeyword(obj, "minLength", path); err != nil {
		return nil, err
	}
	if n.maxLength, err = intKeyword(obj, "maxLength", path); err != nil {
		return nil, err
	}
	if n.minItems, err = intKeyword(obj, "minItems", path); err != nil {
		return nil, err
	}
	if n.maxItems, err = intKeyword(obj, "maxItems", path); err != nil {
		return nil, err
	}
	if n.minimum, err = numKeyword(obj, "minimum", path); err != nil {
		return nil, err
	}
	if n.maximum, err = numKeyword(obj, "maximum", path); err != nil {
		return nil, err
	}
	if raw, ok := obj["items"]; ok {
		node, err := compileNode(raw, path+"[]")
		if err != nil {
			return nil, err
		}
		n.items = node
	}
	if raw, ok := obj["pattern"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("schema %s: pattern must be a string", path)
		}
		n.pattern = &s
	}
	return n, nil
}

func intKeyword(obj map[string]any, key, path string) (*int, error) {
	raw, ok := obj[key]
	if !ok {
		return nil, nil
	}
	num, ok := raw.(json.Number)
	if !ok {
		return nil, fmt.Errorf("schema %s: %s must be a number", path, key)
	}
	v, err := num.Int64()
	if err != nil {
		return nil, fmt.Errorf("schema %s: %s must be an integer", path, key)
	}
	i := int(v)
	return &i, nil
}

func numKeyword(obj map[string]any, key, path string) (*float64, error) {
	raw, ok := obj[key]
	if !ok {
		return nil, nil
	}
	num, ok := raw.(json.Number)
	if !ok {
		return nil, fmt.Errorf("schema %s: %s must be a number", path, key)
	}
	v, err := num.Float64()
	if err != nil {
		return nil, fmt.Errorf("schema %s: %s must be a number", path, key)
	}
	return &v, nil
}

// Raw returns the schema bytes as registered.
func (s *Schema) Raw() json.RawMessage { return s.raw }

// ValidationError locates the first failing property.
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return e.Path + ": " + e.Message }

// Validate checks raw against the schema.
func (s *Schema) Validate(raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return &ValidationError{Path: "$", Message: "参数不是合法的 JSON"}
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return &ValidationError{Path: "$", Message: "参数包含多余内容"}
	}
	return s.root.validate(value, "$")
}

func (n *schemaNode) validate(v any, path string) error {
	if len(n.enum) > 0 {
		body, err := json.Marshal(normalizeNumber(v))
		if err != nil {
			return &ValidationError{Path: path, Message: "值无法序列化"}
		}
		match := false
		for _, candidate := range n.enum {
			if bytes.Equal(candidate, body) {
				match = true
				break
			}
		}
		if !match {
			return &ValidationError{Path: path, Message: "取值不在允许的枚举内"}
		}
	}
	if len(n.types) > 0 && !n.typeMatches(v) {
		return &ValidationError{Path: path, Message: "类型应为 " + strings.Join(n.types, "|")}
	}
	switch value := v.(type) {
	case string:
		length := utf8.RuneCountInString(value)
		if n.minLength != nil && length < *n.minLength {
			return &ValidationError{Path: path, Message: fmt.Sprintf("长度至少 %d 个字符", *n.minLength)}
		}
		if n.maxLength != nil && length > *n.maxLength {
			return &ValidationError{Path: path, Message: fmt.Sprintf("长度最多 %d 个字符", *n.maxLength)}
		}
		if n.pattern != nil {
			if err := matchPattern(*n.pattern, value); err != nil {
				return &ValidationError{Path: path, Message: err.Error()}
			}
		}
	case map[string]any:
		for _, name := range n.required {
			if _, ok := value[name]; !ok {
				return &ValidationError{Path: path + "." + name, Message: "缺少必填字段"}
			}
		}
		for name, child := range n.properties {
			if raw, ok := value[name]; ok {
				if err := child.validate(raw, path+"."+name); err != nil {
					return err
				}
			}
		}
		if n.additionalAllowed != nil && !*n.additionalAllowed {
			names := make([]string, 0, len(value))
			for name := range value {
				if _, known := n.properties[name]; !known {
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				sort.Strings(names)
				return &ValidationError{Path: path, Message: "包含未知参数 " + strings.Join(names, ", ")}
			}
		}
	case []any:
		if n.minItems != nil && len(value) < *n.minItems {
			return &ValidationError{Path: path, Message: fmt.Sprintf("至少需要 %d 项", *n.minItems)}
		}
		if n.maxItems != nil && len(value) > *n.maxItems {
			return &ValidationError{Path: path, Message: fmt.Sprintf("最多允许 %d 项", *n.maxItems)}
		}
		if n.items != nil {
			for i, item := range value {
				if err := n.items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case json.Number:
		f, err := value.Float64()
		if err != nil {
			return &ValidationError{Path: path, Message: "数值无效"}
		}
		if n.minimum != nil && f < *n.minimum {
			return &ValidationError{Path: path, Message: fmt.Sprintf("数值不得小于 %v", *n.minimum)}
		}
		if n.maximum != nil && f > *n.maximum {
			return &ValidationError{Path: path, Message: fmt.Sprintf("数值不得超过 %v", *n.maximum)}
		}
	}
	return nil
}

func (n *schemaNode) typeMatches(v any) bool {
	for _, t := range n.types {
		switch t {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "number":
			if _, ok := v.(json.Number); ok {
				return true
			}
		case "integer":
			if num, ok := v.(json.Number); ok {
				if i, err := num.Int64(); err == nil && float64(i) == mustFloat(num) {
					return true
				}
			}
		case "null":
			if v == nil {
				return true
			}
		}
	}
	return false
}

func mustFloat(n json.Number) float64 {
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return math.NaN()
	}
	return f
}

func normalizeNumber(v any) any {
	if num, ok := v.(json.Number); ok {
		if i, err := num.Int64(); err == nil {
			return i
		}
		if f, err := num.Float64(); err == nil {
			return f
		}
	}
	return v
}

// matchPattern applies an anchored regular expression. The catalog only uses
// simple ^...$ shapes, but using regexp keeps the semantics standard.
func matchPattern(pattern, value string) error {
	ok, err := regexp.MatchString(pattern, value)
	if err != nil {
		return fmt.Errorf("取值格式不合法")
	}
	if !ok {
		return fmt.Errorf("取值不符合要求的格式")
	}
	return nil
}
