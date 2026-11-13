package notes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// Filter and sort limits (SPEC-05 §4.5).
const (
	maxFilterDepth  = 3
	maxFilterLeaves = 30
	dateValueLayout = "2006-01-02"
)

// errDBValidation is the sentinel wrapped by every user-input validation error
// of the database and record services. Callers match it with errors.Is.
var errDBValidation = errors.New("invalid input")

func dbValidationf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errDBValidation, fmt.Sprintf(format, args...))
}

// fieldOperators is the per-type operator whitelist of SPEC-05 §4.5. A leaf
// using any other combination is a validation error.
//
// Filter value encodings (the SPEC leaves the JSON shape open):
//   - eq/neq/contains/starts_with, is/is_not, before/after: a scalar
//   - number between, date between: [low, high] (inclusive)
//   - select is_any_of and multi_select/tags has_*: an array of ids
//   - relation has_any/has_none: an array of {type,id} objects
//   - is_empty/is_not_empty/is_true/is_false: no value
var fieldOperators = map[FieldType][]string{
	FieldTypeText:        {"eq", "neq", "contains", "starts_with", "is_empty", "is_not_empty"},
	FieldTypeURL:         {"eq", "neq", "contains", "starts_with", "is_empty", "is_not_empty"},
	FieldTypeNumber:      {"eq", "neq", "gt", "gte", "lt", "lte", "between", "is_empty", "is_not_empty"},
	FieldTypeDate:        {"before", "after", "between", "is_empty", "is_not_empty"},
	FieldTypeSelect:      {"is", "is_not", "is_any_of", "is_empty", "is_not_empty"},
	FieldTypeMultiSelect: {"has_any", "has_all", "has_none", "is_empty"},
	FieldTypeTags:        {"has_any", "has_all", "has_none", "is_empty"},
	FieldTypeCheckbox:    {"is_true", "is_false"},
	FieldTypeRelation:    {"has_any", "has_none", "is_empty"},
}

func operatorAllowed(t FieldType, op string) bool {
	for _, candidate := range fieldOperators[t] {
		if candidate == op {
			return true
		}
	}
	return false
}

// predicate is one compiled filter node evaluated in Go.
//
// Why Go instead of SQL: the SPEC requires Unicode case-insensitive text
// matching, null-last ordering and JSON array semantics (has_any/has_all)
// that SQLite's json_extract cannot express exactly. ListRecords therefore
// runs one parameterized SELECT over the database's rows and applies the
// validated predicate and sort here. User values never reach SQL text: the
// only variables are bound placeholders.
type predicate interface {
	match(values map[string]json.RawMessage) bool
}

type filterGroup struct {
	op       LogicOp
	children []predicate
}

func (g *filterGroup) match(values map[string]json.RawMessage) bool {
	if g.op == LogicAnd {
		for _, child := range g.children {
			if !child.match(values) {
				return false
			}
		}
		return true
	}
	for _, child := range g.children {
		if child.match(values) {
			return true
		}
	}
	return false
}

type filterLeaf struct {
	fieldID string
	typ     FieldType
	op      string

	argText string
	argNum  float64
	argNum2 float64
	argIDs  []string
	argRefs []EntityRef
}

// compileFilter validates a filter tree against the live fields of one
// database and returns the compiled predicate. A nil predicate means "no
// constraint": the caller passed no filter, or every leaf referenced a
// soft-deleted/unknown field (SPEC-05 §4.4 ignores those instead of failing).
func compileFilter(root *FilterNode, fields map[string]*Field) (predicate, error) {
	if root == nil {
		return nil, nil
	}
	if err := checkFilterDepth(*root, 1); err != nil {
		return nil, err
	}
	if leaves := countFilterLeaves(*root); leaves > maxFilterLeaves {
		return nil, dbValidationf("filter has %d leaves, at most %d are allowed", leaves, maxFilterLeaves)
	}
	p, kept, err := compileFilterNode(root, fields)
	if err != nil {
		return nil, err
	}
	if !kept {
		return nil, nil
	}
	return p, nil
}

func checkFilterDepth(n FilterNode, depth int) error {
	if depth > maxFilterDepth {
		return dbValidationf("filter nesting depth %d exceeds the maximum of %d", depth, maxFilterDepth)
	}
	for i := range n.Children {
		if err := checkFilterDepth(n.Children[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}

func countFilterLeaves(n FilterNode) int {
	if len(n.Children) == 0 {
		return 1
	}
	total := 0
	for i := range n.Children {
		total += countFilterLeaves(n.Children[i])
	}
	return total
}

func compileFilterNode(n *FilterNode, fields map[string]*Field) (predicate, bool, error) {
	if len(n.Children) == 0 {
		if n.FieldID == "" {
			return nil, false, dbValidationf("filter leaf has no field id")
		}
		if n.Op != "" {
			return nil, false, dbValidationf("filter leaf %q must not carry a logic operator", n.FieldID)
		}
		field, ok := fields[n.FieldID]
		if !ok {
			return nil, false, nil // soft-deleted or unknown field: ignored (§4.4)
		}
		leaf, err := compileLeaf(field, n.Operator, n.Value)
		if err != nil {
			return nil, false, err
		}
		return leaf, true, nil
	}
	if n.FieldID != "" {
		return nil, false, dbValidationf("filter group must not carry a field id")
	}
	if n.Op != LogicAnd && n.Op != LogicOr {
		return nil, false, dbValidationf("filter group has invalid logic operator %q", n.Op)
	}
	group := &filterGroup{op: n.Op}
	for i := range n.Children {
		child, kept, err := compileFilterNode(&n.Children[i], fields)
		if err != nil {
			return nil, false, err
		}
		if kept {
			group.children = append(group.children, child)
		}
	}
	if len(group.children) == 0 {
		return nil, false, nil
	}
	return group, true, nil
}

func compileLeaf(field *Field, operator string, value json.RawMessage) (*filterLeaf, error) {
	if operator == "" {
		return nil, dbValidationf("filter on field %q has no operator", field.ID)
	}
	if !operatorAllowed(field.Type, operator) {
		return nil, dbValidationf("operator %q is not allowed for field type %q", operator, field.Type)
	}
	leaf := &filterLeaf{fieldID: field.ID, typ: field.Type, op: operator}
	switch field.Type {
	case FieldTypeText, FieldTypeURL:
		switch operator {
		case "eq", "neq", "contains", "starts_with":
			s, err := decodeJSONString(value)
			if err != nil {
				return nil, dbValidationf("operator %q needs a string value: %v", operator, err)
			}
			leaf.argText = s
		}
	case FieldTypeNumber:
		switch operator {
		case "eq", "neq", "gt", "gte", "lt", "lte":
			n, err := decodeJSONNumber(value)
			if err != nil {
				return nil, dbValidationf("operator %q needs a number value: %v", operator, err)
			}
			leaf.argNum = n
		case "between":
			low, high, err := decodeNumberPair(value)
			if err != nil {
				return nil, dbValidationf("between needs [low, high]: %v", err)
			}
			leaf.argNum, leaf.argNum2 = low, high
		}
	case FieldTypeDate:
		switch operator {
		case "before", "after":
			d, err := decodeJSONDate(value)
			if err != nil {
				return nil, dbValidationf("operator %q needs a YYYY-MM-DD value: %v", operator, err)
			}
			leaf.argText = d
		case "between":
			low, high, err := decodeDatePair(value)
			if err != nil {
				return nil, dbValidationf("between needs [from, to] as YYYY-MM-DD: %v", err)
			}
			leaf.argIDs = []string{low, high}
		}
	case FieldTypeSelect:
		switch operator {
		case "is", "is_not":
			s, err := decodeJSONString(value)
			if err != nil || s == "" {
				return nil, dbValidationf("operator %q needs an option id", operator)
			}
			leaf.argText = s
		case "is_any_of":
			idsList, err := decodeStringList(value)
			if err != nil {
				return nil, dbValidationf("is_any_of needs an array of option ids: %v", err)
			}
			leaf.argIDs = idsList
		}
	case FieldTypeMultiSelect, FieldTypeTags:
		switch operator {
		case "has_any", "has_all", "has_none":
			idsList, err := decodeStringList(value)
			if err != nil {
				return nil, dbValidationf("operator %q needs an array of ids: %v", operator, err)
			}
			leaf.argIDs = idsList
		}
	case FieldTypeRelation:
		switch operator {
		case "has_any", "has_none":
			refs, err := decodeRelationRefs(value)
			if err != nil {
				return nil, dbValidationf("operator %q needs [{type,id}] values: %v", operator, err)
			}
			leaf.argRefs = refs
		}
	case FieldTypeCheckbox:
		// is_true / is_false carry no value.
	}
	return leaf, nil
}

func (l *filterLeaf) match(values map[string]json.RawMessage) bool {
	raw, present := values[l.fieldID]
	if !present || isJSONNull(raw) {
		return l.matchEmpty()
	}
	switch l.typ {
	case FieldTypeText, FieldTypeURL:
		s, ok := rawString(raw)
		if !ok {
			return l.matchEmpty()
		}
		switch l.op {
		case "eq":
			return strings.EqualFold(s, l.argText)
		case "neq":
			return !strings.EqualFold(s, l.argText)
		case "contains":
			return strings.Contains(foldText(s), foldText(l.argText))
		case "starts_with":
			return strings.HasPrefix(foldText(s), foldText(l.argText))
		case "is_empty":
			return s == ""
		case "is_not_empty":
			return s != ""
		}
	case FieldTypeNumber:
		n, ok := rawNumber(raw)
		if !ok {
			return l.matchEmpty()
		}
		switch l.op {
		case "eq":
			return n == l.argNum
		case "neq":
			return n != l.argNum
		case "gt":
			return n > l.argNum
		case "gte":
			return n >= l.argNum
		case "lt":
			return n < l.argNum
		case "lte":
			return n <= l.argNum
		case "between":
			return n >= l.argNum && n <= l.argNum2
		}
	case FieldTypeDate:
		s, ok := rawString(raw)
		if !ok || !validDate(s) {
			return l.matchEmpty()
		}
		switch l.op {
		case "before":
			return s < l.argText
		case "after":
			return s > l.argText
		case "between":
			return s >= l.argIDs[0] && s <= l.argIDs[1]
		}
	case FieldTypeSelect:
		s, ok := rawString(raw)
		if !ok || s == "" {
			return l.matchEmpty()
		}
		switch l.op {
		case "is":
			return s == l.argText
		case "is_not":
			return s != l.argText
		case "is_any_of":
			return containsString(l.argIDs, s)
		}
	case FieldTypeMultiSelect, FieldTypeTags:
		list, ok := rawStringList(raw)
		if !ok {
			return l.matchEmpty()
		}
		switch l.op {
		case "has_any":
			return intersects(list, l.argIDs)
		case "has_all":
			return containsAll(list, l.argIDs)
		case "has_none":
			return !intersects(list, l.argIDs)
		case "is_empty":
			return len(list) == 0
		}
	case FieldTypeCheckbox:
		b, _ := rawBool(raw)
		switch l.op {
		case "is_true":
			return b
		case "is_false":
			return !b
		}
	case FieldTypeRelation:
		refs, ok := rawRelationRefs(raw)
		if !ok {
			return l.matchEmpty()
		}
		switch l.op {
		case "has_any":
			return refsIntersect(refs, l.argRefs)
		case "has_none":
			return !refsIntersect(refs, l.argRefs)
		case "is_empty":
			return len(refs) == 0
		}
	}
	return false
}

// matchEmpty applies the missing-value semantics of §4.4: "no key" is empty.
// eq/neq and is/is_not are exact complements, so neq/is_not also match rows
// where the field has no value.
func (l *filterLeaf) matchEmpty() bool {
	switch l.op {
	case "is_empty":
		return true
	case "is_not_empty":
		return false
	case "neq", "is_not", "has_none":
		return true
	case "is_false":
		return true
	case "is_true":
		return false
	}
	return false
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

// sortSpec is one compiled sort key. fieldID is empty for the built-ins.
type sortSpec struct {
	builtin string
	fieldID string
	typ     FieldType
	desc    bool
}

// compileSorts keeps built-in keys and live fields. Soft-deleted/unknown field
// ids are ignored (§4.4: sorting ignores them) instead of failing the query.
func compileSorts(keys []SortKey, fields map[string]*Field) []sortSpec {
	specs := make([]sortSpec, 0, len(keys))
	for _, key := range keys {
		switch key.FieldID {
		case "":
			continue
		case SortTitle, SortCreatedAt, SortUpdatedAt:
			specs = append(specs, sortSpec{builtin: key.FieldID, desc: key.Desc})
			continue
		}
		field, ok := fields[key.FieldID]
		if !ok {
			continue
		}
		specs = append(specs, sortSpec{fieldID: field.ID, typ: field.Type, desc: key.Desc})
	}
	return specs
}

// sortRow is one candidate record during filtering/sorting.
type sortRow struct {
	id        string
	title     string
	createdAt time.Time
	updatedAt time.Time
	values    map[string]json.RawMessage
}

// sortValue is a type-tagged comparable value.
type sortValue struct {
	kind byte // 's' string, 'n' number, 't' time, 'b' bool
	s    string
	n    float64
	t    time.Time
	b    bool
}

func (r *sortRow) key(spec sortSpec) (sortValue, bool) {
	switch spec.builtin {
	case SortTitle:
		// db_records.title is the projection of the title field (D-05-06).
		return sortValue{kind: 's', s: r.title}, true
	case SortCreatedAt:
		return sortValue{kind: 't', t: r.createdAt}, true
	case SortUpdatedAt:
		return sortValue{kind: 't', t: r.updatedAt}, true
	}
	// Array-typed fields compare by their first stored element; empty arrays
	// count as null and therefore sort last.
	raw, ok := r.values[spec.fieldID]
	if !ok || isJSONNull(raw) {
		return sortValue{}, false
	}
	switch spec.typ {
	case FieldTypeText, FieldTypeURL, FieldTypeDate, FieldTypeSelect:
		s, ok := rawString(raw)
		if !ok {
			return sortValue{}, false
		}
		return sortValue{kind: 's', s: s}, true
	case FieldTypeNumber:
		n, ok := rawNumber(raw)
		if !ok {
			return sortValue{}, false
		}
		return sortValue{kind: 'n', n: n}, true
	case FieldTypeCheckbox:
		b, ok := rawBool(raw)
		if !ok {
			return sortValue{}, false
		}
		return sortValue{kind: 'b', b: b}, true
	case FieldTypeMultiSelect, FieldTypeTags:
		list, ok := rawStringList(raw)
		if !ok || len(list) == 0 {
			return sortValue{}, false
		}
		return sortValue{kind: 's', s: list[0]}, true
	case FieldTypeRelation:
		refs, ok := rawRelationRefs(raw)
		if !ok || len(refs) == 0 {
			return sortValue{}, false
		}
		return sortValue{kind: 's', s: string(refs[0].Type) + ":" + refs[0].ID}, true
	}
	return sortValue{}, false
}

func compareSortValues(a, b sortValue) int {
	switch a.kind {
	case 's':
		return strings.Compare(a.s, b.s)
	case 'n':
		switch {
		case a.n < b.n:
			return -1
		case a.n > b.n:
			return 1
		default:
			return 0
		}
	case 't':
		return a.t.Compare(b.t)
	case 'b':
		switch {
		case a.b == b.b:
			return 0
		case !a.b:
			return -1
		default:
			return 1
		}
	}
	return 0
}

// sortRows orders rows by the compiled keys. Empty values sort last in both
// directions; ties fall back to the record id ascending for a stable order.
func sortRows(rows []*sortRow, specs []sortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, spec := range specs {
			a, aok := rows[i].key(spec)
			b, bok := rows[j].key(spec)
			if !aok && !bok {
				continue
			}
			if !aok {
				return false
			}
			if !bok {
				return true
			}
			cmp := compareSortValues(a, b)
			if cmp == 0 {
				continue
			}
			if spec.desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return rows[i].id < rows[j].id
	})
}

// ---------------------------------------------------------------------------
// JSON value helpers
// ---------------------------------------------------------------------------

func isJSONNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

func foldText(s string) string { return strings.ToLower(s) }

func validDate(s string) bool {
	t, err := time.Parse(dateValueLayout, s)
	return err == nil && t.Format(dateValueLayout) == s
}

func decodeJSONString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errors.New("value must be a JSON string")
	}
	return s, nil
}

func decodeJSONNumber(raw json.RawMessage) (float64, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, errors.New("value must be a JSON number")
	}
	num, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("value must be a JSON number")
	}
	f, err := num.Float64()
	if err != nil {
		return 0, errors.New("value must be a JSON number")
	}
	return f, nil
}

func decodeJSONDate(raw json.RawMessage) (string, error) {
	s, err := decodeJSONString(raw)
	if err != nil {
		return "", err
	}
	if !validDate(s) {
		return "", fmt.Errorf("%q is not a strict YYYY-MM-DD date", s)
	}
	return s, nil
}

func decodeStringList(raw json.RawMessage) ([]string, error) {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("value must be a JSON array of strings")
	}
	if len(list) == 0 {
		return nil, errors.New("value must contain at least one item")
	}
	for _, item := range list {
		if item == "" {
			return nil, errors.New("array items must be non-empty")
		}
	}
	return list, nil
}

func decodeNumberPair(raw json.RawMessage) (float64, float64, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 2 {
		return 0, 0, errors.New("value must be a two-element JSON array")
	}
	low, err := decodeJSONNumber(list[0])
	if err != nil {
		return 0, 0, err
	}
	high, err := decodeJSONNumber(list[1])
	if err != nil {
		return 0, 0, err
	}
	return low, high, nil
}

func decodeDatePair(raw json.RawMessage) (string, string, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 2 {
		return "", "", errors.New("value must be a two-element JSON array")
	}
	low, err := decodeJSONDate(list[0])
	if err != nil {
		return "", "", err
	}
	high, err := decodeJSONDate(list[1])
	if err != nil {
		return "", "", err
	}
	return low, high, nil
}

// relationTargetTypes are the entity types a relation field may point at
// (SPEC-05 §4.4).
var relationTargetTypes = []schema.EntityType{
	schema.EntityPaper,
	schema.EntityNote,
	schema.EntityIdea,
	schema.EntityExperiment,
	schema.EntityDBRecord,
}

func relationTargetAllowed(t schema.EntityType) bool {
	for _, candidate := range relationTargetTypes {
		if candidate == t {
			return true
		}
	}
	return false
}

func decodeRelationRefs(raw json.RawMessage) ([]EntityRef, error) {
	var items []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("value must be a JSON array of {type,id} objects")
	}
	if len(items) == 0 {
		return nil, errors.New("value must contain at least one target")
	}
	refs := make([]EntityRef, 0, len(items))
	for _, item := range items {
		entityType := schema.EntityType(item.Type)
		if !relationTargetAllowed(entityType) {
			return nil, fmt.Errorf("target type %q is not allowed", item.Type)
		}
		if !ids.Valid(item.ID) {
			return nil, fmt.Errorf("target id %q is not a UUIDv7", item.ID)
		}
		refs = append(refs, EntityRef{Type: entityType, ID: item.ID})
	}
	return refs, nil
}

func rawString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func rawNumber(raw json.RawMessage) (float64, bool) {
	n, err := decodeJSONNumber(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

func rawBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func rawStringList(raw json.RawMessage) ([]string, bool) {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false
	}
	return list, true
}

func rawRelationRefs(raw json.RawMessage) ([]EntityRef, bool) {
	var items []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	refs := make([]EntityRef, 0, len(items))
	for _, item := range items {
		refs = append(refs, EntityRef{Type: schema.EntityType(item.Type), ID: item.ID})
	}
	return refs, true
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func intersects(have, want []string) bool {
	for _, item := range want {
		if containsString(have, item) {
			return true
		}
	}
	return false
}

func containsAll(have, want []string) bool {
	for _, item := range want {
		if !containsString(have, item) {
			return false
		}
	}
	return true
}

func refsIntersect(have, want []EntityRef) bool {
	for _, w := range want {
		for _, h := range have {
			if h.Type == w.Type && h.ID == w.ID {
				return true
			}
		}
	}
	return false
}
