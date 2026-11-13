package notes

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"researchos/internal/foundation/schema"
)

func dbLeaf(fieldID, operator string, value any) FilterNode {
	node := FilterNode{FieldID: fieldID, Operator: operator}
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		node.Value = encoded
	}
	return node
}

func dbGroup(op LogicOp, children ...FilterNode) FilterNode {
	return FilterNode{Op: op, Children: children}
}

func dbFilterIDs(t *testing.T, f *dbFixture, databaseID string, filter *FilterNode) []string {
	t.Helper()
	page, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: databaseID, Filter: filter})
	if err != nil {
		t.Fatalf("ListRecords with filter: %v", err)
	}
	out := make([]string, 0, len(page.Records))
	for _, record := range page.Records {
		out = append(out, record.ID)
	}
	if page.Total != len(page.Records) {
		t.Fatalf("total = %d but %d rows returned", page.Total, len(page.Records))
	}
	return out
}

func dbAssertIDs(t *testing.T, name string, got []string, want ...*Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d rows %v, want %d", name, len(got), got, len(want))
	}
	seen := make(map[string]bool, len(got))
	for _, id := range got {
		seen[id] = true
	}
	for _, record := range want {
		if !seen[record.ID] {
			t.Fatalf("%s: missing record %s in %v", name, record.ID, got)
		}
	}
}

func TestFilterValidationErrors(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Errors")
	textField := f.createField(database.ID, "Text", FieldTypeText, "")
	numberField := f.createField(database.ID, "Score", FieldTypeNumber, "")
	dateField := f.createField(database.ID, "When", FieldTypeDate, "")
	checkboxField := f.createField(database.ID, "Done", FieldTypeCheckbox, "")
	selectField := f.createField(database.ID, "Kind", FieldTypeSelect, `{"options":[{"name":"A"}]}`)
	multiField := f.createField(database.ID, "Multi", FieldTypeMultiSelect, `{"options":[{"name":"A"}]}`)
	relationField := f.createField(database.ID, "Ref", FieldTypeRelation, `{"targets":["note"]}`)

	target := dbLeaf(textField.ID, "eq", "x")
	validID := "11111111-1111-7111-8111-111111111111"
	manyLeaves := make([]FilterNode, 0, maxFilterLeaves+1)
	for i := range maxFilterLeaves + 1 {
		manyLeaves = append(manyLeaves, dbLeaf(textField.ID, "eq", fmt.Sprintf("v%d", i)))
	}

	cases := []struct {
		name   string
		filter *FilterNode
	}{
		{"text gt", new(dbLeaf(textField.ID, "gt", "x"))},
		{"number contains", new(dbLeaf(numberField.ID, "contains", "x"))},
		{"date eq", new(dbLeaf(dateField.ID, "eq", "2026-01-01"))},
		{"checkbox is", new(dbLeaf(checkboxField.ID, "is", true))},
		{"select gt", new(dbLeaf(selectField.ID, "gt", "x"))},
		{"multi is", new(dbLeaf(multiField.ID, "is", "x"))},
		{"relation has_all", new(dbLeaf(relationField.ID, "has_all", []string{validID}))},
		{"number bad value", new(dbLeaf(numberField.ID, "eq", "not-a-number"))},
		{"number short pair", new(dbLeaf(numberField.ID, "between", []float64{1}))},
		{"date impossible", new(dbLeaf(dateField.ID, "before", "2026-13-01"))},
		{"select any_of not array", new(dbLeaf(selectField.ID, "is_any_of", validID))},
		{"multi empty array", new(dbLeaf(multiField.ID, "has_any", []string{}))},
		{"relation bogus type", new(dbLeaf(relationField.ID, "has_any", []map[string]string{{"type": "bogus", "id": validID}}))},
		{"unknown logic op", new(dbGroup("xor", target))},
		{"empty leaf", new(FilterNode{})},
		{"group with field id", new(FilterNode{Op: LogicAnd, FieldID: textField.ID, Children: []FilterNode{target}})},
		{"leaf with op", new(FilterNode{FieldID: textField.ID, Operator: "eq", Op: LogicAnd, Value: json.RawMessage(`"x"`)})},
		{"too deep", new(dbGroup(LogicAnd,
			dbGroup(LogicAnd,
				dbGroup(LogicAnd, target))))},
		{"too many leaves", new(dbGroup(LogicAnd, manyLeaves...))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID, Filter: tc.filter})
			if !errors.Is(err, errDBValidation) {
				t.Fatalf("error = %v, want validation error", err)
			}
		})
	}

	// A valid depth-3 tree is accepted.
	valid := dbGroup(LogicAnd, dbGroup(LogicOr, target, dbLeaf(textField.ID, "contains", "y")))
	if _, err := f.records.ListRecords(f.ctx, RecordQuery{DatabaseID: database.ID, Filter: &valid}); err != nil {
		t.Fatalf("valid depth-3 filter: %v", err)
	}
}

func TestFilterIgnoresDeletedAndUnknownFields(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Ignored")
	titleID := f.titleField(database.ID).ID
	tempField := f.createField(database.ID, "Temp", FieldTypeText, "")

	one := f.createRecord(database.ID, map[string]any{titleID: "one", tempField.ID: "x"})
	two := f.createRecord(database.ID, map[string]any{titleID: "two"})

	if err := f.databases.DeleteField(f.ctx, tempField.ID, schema.ActorUser); err != nil {
		t.Fatalf("DeleteField: %v", err)
	}

	got := dbFilterIDs(t, f, database.ID, new(dbLeaf(tempField.ID, "eq", "x")))
	dbAssertIDs(t, "deleted field", got, one, two)

	unknown := "22222222-2222-7222-8222-222222222222"
	got = dbFilterIDs(t, f, database.ID, new(dbLeaf(unknown, "eq", "x")))
	dbAssertIDs(t, "unknown field", got, one, two)

	// A pruned AND child leaves the remaining condition effective.
	got = dbFilterIDs(t, f, database.ID, new(dbGroup(LogicAnd,
		dbLeaf(unknown, "eq", "x"),
		dbLeaf(titleID, "eq", "two"))))
	dbAssertIDs(t, "pruned AND", got, two)

	// Unknown sort keys are ignored rather than fatal.
	page, err := f.records.ListRecords(f.ctx, RecordQuery{
		DatabaseID: database.ID,
		Sorts:      []SortKey{{FieldID: unknown}, {FieldID: tempField.ID}},
	})
	if err != nil {
		t.Fatalf("ListRecords with ignored sorts: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("rows with ignored sorts = %d, want 2", len(page.Records))
	}
}

func TestFilterSortRowsNullsLastUnit(t *testing.T) {
	numberID := "f-number"
	fields := map[string]*Field{
		numberID: {ID: numberID, Type: FieldTypeNumber},
	}
	raw := func(value any) json.RawMessage {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return encoded
	}
	rows := []*sortRow{
		{id: "b", title: "b", values: map[string]json.RawMessage{numberID: raw(2)}},
		{id: "a", title: "x", values: map[string]json.RawMessage{numberID: raw(1)}},
		{id: "c", title: "c", values: map[string]json.RawMessage{}},
		{id: "d", title: "y", values: map[string]json.RawMessage{numberID: raw(1)}},
	}
	order := func() string {
		out := ""
		for _, row := range rows {
			out += row.id
		}
		return out
	}

	asc := compileSorts([]SortKey{{FieldID: numberID}}, fields)
	sortRows(rows, asc)
	if got := order(); got != "adbc" {
		t.Fatalf("ascending order (id tiebreak, null last) = %q, want adbc", got)
	}

	desc := compileSorts([]SortKey{{FieldID: numberID, Desc: true}}, fields)
	sortRows(rows, desc)
	if got := order(); got != "badc" {
		t.Fatalf("descending order (null still last) = %q, want badc", got)
	}

	multi := compileSorts([]SortKey{{FieldID: numberID}, {FieldID: SortTitle, Desc: true}}, fields)
	sortRows(rows, multi)
	if got := order(); got != "dabc" {
		t.Fatalf("multi-key order = %q, want dabc", got)
	}
}

// TestFilterOperatorMatrix creates one database with every field type and
// three rows (one fully populated, one partially, one empty) and checks every
// operator family end to end through ListRecords.
func TestFilterOperatorMatrix(t *testing.T) {
	f := dbNewFixture(t)
	database := f.createDatabase("Matrix")
	titleID := f.titleField(database.ID).ID
	textField := f.createField(database.ID, "Label", FieldTypeText, "")
	numberField := f.createField(database.ID, "Score", FieldTypeNumber, "")
	dateField := f.createField(database.ID, "When", FieldTypeDate, "")
	selectField := f.createField(database.ID, "Kind", FieldTypeSelect, `{"options":[{"name":"A"},{"name":"B"}]}`)
	selectCfg, err := parseFieldConfig(selectField.Type, selectField.Config)
	if err != nil {
		t.Fatalf("select config: %v", err)
	}
	optionA, optionB := selectCfg.Options[0].ID, selectCfg.Options[1].ID
	multiField := f.createField(database.ID, "Multi", FieldTypeMultiSelect, `{"options":[{"name":"A"},{"name":"B"}]}`)
	multiCfg, err := parseFieldConfig(multiField.Type, multiField.Config)
	if err != nil {
		t.Fatalf("multi config: %v", err)
	}
	multiA, multiB := multiCfg.Options[0].ID, multiCfg.Options[1].ID
	checkboxField := f.createField(database.ID, "Done", FieldTypeCheckbox, "")
	urlField := f.createField(database.ID, "Link", FieldTypeURL, "")
	relationField := f.createField(database.ID, "Ref", FieldTypeRelation, `{"targets":["note"]}`)
	tagsField := f.createField(database.ID, "Tags", FieldTypeTags, "")

	targetNote := f.createNote(schema.NoteScratchpad, "Target")
	tagOne := f.createTag("one")
	tagTwo := f.createTag("two")

	full := f.createRecord(database.ID, map[string]any{
		titleID:          "Alpha",
		textField.ID:     "Alpha",
		numberField.ID:   5,
		dateField.ID:     "2026-01-10",
		selectField.ID:   optionA,
		multiField.ID:    []string{multiA, multiB},
		checkboxField.ID: true,
		urlField.ID:      "https://alpha.example",
		relationField.ID: []map[string]string{{"type": "note", "id": targetNote}},
		tagsField.ID:     []string{tagOne},
	})
	partial := f.createRecord(database.ID, map[string]any{
		titleID:          "beta",
		textField.ID:     "beta",
		numberField.ID:   10,
		dateField.ID:     "2026-02-20",
		selectField.ID:   optionB,
		multiField.ID:    []string{multiB},
		checkboxField.ID: false,
		urlField.ID:      "",
		relationField.ID: []map[string]string{},
		tagsField.ID:     []string{tagTwo},
	})
	empty := f.createRecord(database.ID, map[string]any{})

	cases := []struct {
		name   string
		filter FilterNode
		want   []*Record
	}{
		{"text eq case-insensitive", dbLeaf(textField.ID, "eq", "alpha"), []*Record{full}},
		{"text neq is complement", dbLeaf(textField.ID, "neq", "alpha"), []*Record{partial, empty}},
		{"text contains folds case", dbLeaf(textField.ID, "contains", "ET"), []*Record{partial}},
		{"text starts_with", dbLeaf(textField.ID, "starts_with", "AL"), []*Record{full}},
		{"text is_empty", dbLeaf(textField.ID, "is_empty", nil), []*Record{empty}},
		{"text is_not_empty", dbLeaf(textField.ID, "is_not_empty", nil), []*Record{full, partial}},

		{"url eq", dbLeaf(urlField.ID, "eq", "https://alpha.example"), []*Record{full}},
		{"url is_empty", dbLeaf(urlField.ID, "is_empty", nil), []*Record{partial, empty}},

		{"number eq", dbLeaf(numberField.ID, "eq", 5), []*Record{full}},
		{"number neq", dbLeaf(numberField.ID, "neq", 5), []*Record{partial, empty}},
		{"number gt", dbLeaf(numberField.ID, "gt", 5), []*Record{partial}},
		{"number gte", dbLeaf(numberField.ID, "gte", 10), []*Record{partial}},
		{"number lt", dbLeaf(numberField.ID, "lt", 6), []*Record{full}},
		{"number lte", dbLeaf(numberField.ID, "lte", 5), []*Record{full}},
		{"number between", dbLeaf(numberField.ID, "between", []float64{5, 10}), []*Record{full, partial}},
		{"number is_empty", dbLeaf(numberField.ID, "is_empty", nil), []*Record{empty}},

		{"date before", dbLeaf(dateField.ID, "before", "2026-02-01"), []*Record{full}},
		{"date after", dbLeaf(dateField.ID, "after", "2026-02-01"), []*Record{partial}},
		{"date between", dbLeaf(dateField.ID, "between", []string{"2026-01-01", "2026-02-01"}), []*Record{full}},
		{"date is_empty", dbLeaf(dateField.ID, "is_empty", nil), []*Record{empty}},

		{"select is", dbLeaf(selectField.ID, "is", optionA), []*Record{full}},
		{"select is_not", dbLeaf(selectField.ID, "is_not", optionA), []*Record{partial, empty}},
		{"select is_any_of", dbLeaf(selectField.ID, "is_any_of", []string{optionA, optionB}), []*Record{full, partial}},
		{"select is_empty", dbLeaf(selectField.ID, "is_empty", nil), []*Record{empty}},

		{"multi has_any", dbLeaf(multiField.ID, "has_any", []string{multiA}), []*Record{full}},
		{"multi has_all", dbLeaf(multiField.ID, "has_all", []string{multiA, multiB}), []*Record{full}},
		{"multi has_none", dbLeaf(multiField.ID, "has_none", []string{multiA}), []*Record{partial, empty}},
		{"multi is_empty", dbLeaf(multiField.ID, "is_empty", nil), []*Record{empty}},

		{"checkbox is_true", dbLeaf(checkboxField.ID, "is_true", nil), []*Record{full}},
		{"checkbox is_false", dbLeaf(checkboxField.ID, "is_false", nil), []*Record{partial, empty}},

		{"relation has_any", dbLeaf(relationField.ID, "has_any", []map[string]string{{"type": "note", "id": targetNote}}), []*Record{full}},
		{"relation has_none", dbLeaf(relationField.ID, "has_none", []map[string]string{{"type": "note", "id": targetNote}}), []*Record{partial, empty}},
		{"relation is_empty", dbLeaf(relationField.ID, "is_empty", nil), []*Record{partial, empty}},

		{"tags has_any", dbLeaf(tagsField.ID, "has_any", []string{tagOne}), []*Record{full}},
		{"tags has_all", dbLeaf(tagsField.ID, "has_all", []string{tagOne}), []*Record{full}},
		{"tags has_none", dbLeaf(tagsField.ID, "has_none", []string{tagOne}), []*Record{partial, empty}},
		{"tags is_empty", dbLeaf(tagsField.ID, "is_empty", nil), []*Record{empty}},

		{"and combination", dbGroup(LogicAnd,
			dbLeaf(numberField.ID, "gte", 5),
			dbLeaf(selectField.ID, "is", optionB)), []*Record{partial}},
		{"or combination", dbGroup(LogicOr,
			dbLeaf(textField.ID, "eq", "beta"),
			dbLeaf(urlField.ID, "is_empty", nil)), []*Record{partial, empty}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := tc.filter
			dbAssertIDs(t, tc.name, dbFilterIDs(t, f, database.ID, &filter), tc.want...)
		})
	}
}
