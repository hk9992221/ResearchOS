package zotero

import (
	"encoding/json"
	"testing"

	"researchos/internal/literature"
)

func TestToPaperMetadata(t *testing.T) {
	item := literature.ZoteroItem{
		Key:              "ABCD1234",
		Version:          1234,
		ItemType:         "journalArticle",
		Title:            "论文标题",
		Date:             "2024-05-01",
		PublicationTitle: "Journal Name",
		DOI:              "10.xxxx/xxxxx",
		URL:              "https://example.org/x",
		AbstractNote:     "摘要原文",
		Language:         "en",
		Extra:            "Citation Key: abc",
		Creators: []literature.ZoteroCreator{
			{FirstName: "Ada", LastName: "Lovelace", CreatorType: "author"},
			{Name: "某研究所", CreatorType: "author"},
			{FirstName: "三", LastName: "张", CreatorType: "author"},
		},
		Tags:        []string{"anomaly detection", "physics-informed"},
		Collections: []string{"COLL1234"},
		Raw: map[string]any{
			"tags": []any{
				map[string]any{"tag": "anomaly detection", "type": float64(0)},
				map[string]any{"tag": "physics-informed", "type": float64(1)},
			},
		},
	}

	meta := ToPaperMetadata(item)
	if meta.Key != "ABCD1234" || meta.Version != 1234 || meta.ItemType != "journalArticle" {
		t.Errorf("identity = %+v", meta)
	}
	if meta.Title != "论文标题" || meta.Date != "2024-05-01" || meta.PublicationTitle != "Journal Name" {
		t.Errorf("fields = %+v", meta)
	}
	if meta.DOI != "10.xxxx/xxxxx" || meta.URL != "https://example.org/x" || meta.AbstractNote != "摘要原文" {
		t.Errorf("fields = %+v", meta)
	}
	if meta.Language != "en" || meta.Extra != "Citation Key: abc" {
		t.Errorf("fields = %+v", meta)
	}
	if meta.Year != 2024 {
		t.Errorf("year = %d", meta.Year)
	}
	if meta.Authors != "Lovelace, Ada, 某研究所, 张, 三" {
		t.Errorf("authors = %q", meta.Authors)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != (literature.ZoteroTag{Tag: "anomaly detection", Type: 0}) ||
		meta.Tags[1] != (literature.ZoteroTag{Tag: "physics-informed", Type: 1}) {
		t.Errorf("tags = %+v", meta.Tags)
	}
	if len(meta.Collections) != 1 || meta.Collections[0] != "COLL1234" {
		t.Errorf("collections = %+v", meta.Collections)
	}

	// The caller's slices must not be aliased.
	item.Creators[0].LastName = "Changed"
	if meta.Creators[0].LastName != "Lovelace" {
		t.Error("creators were aliased")
	}
}

func TestToPaperMetadataRawOnlyTags(t *testing.T) {
	item := literature.ZoteroItem{
		Tags: nil,
		Raw: map[string]any{
			"tags": []any{
				map[string]any{"tag": "automatic", "type": float64(1)},
			},
		},
	}
	meta := ToPaperMetadata(item)
	if len(meta.Tags) != 1 || meta.Tags[0].Tag != "automatic" || meta.Tags[0].Type != 1 {
		t.Fatalf("tags = %+v", meta.Tags)
	}
}

func TestDeriveYear(t *testing.T) {
	cases := map[string]int{
		"2024-05-01":   2024,
		"May 2019":     2019,
		"2021-11":      2021,
		"circa 1999":   1999,
		"no year here": 0,
		"":             0,
		"2024年5月":      2024,
	}
	for date, want := range cases {
		if got := deriveYear(date); got != want {
			t.Errorf("deriveYear(%q) = %d, want %d", date, got, want)
		}
	}
}

func TestMetadataJSONPreservesUnknownFields(t *testing.T) {
	item := literature.ZoteroItem{
		Key:         "ABCD1234",
		Version:     7,
		ItemType:    "journalArticle",
		Title:       "Normalized",
		Date:        "2020",
		Tags:        []string{"x"},
		Collections: []string{"COLL1"},
		LibraryID:   "0",
		Deleted:     true,
		Raw: map[string]any{
			"title":      "raw title",
			"callNumber": "QA76",
			"number":     float64(3),
			"tags":       []any{map[string]any{"tag": "x", "type": float64(1)}},
			"deleted":    true,
			"libraryID":  "0",
			"Raw":        map[string]any{},
		},
	}
	body, err := MetadataJSON(item)
	if err != nil {
		t.Fatalf("MetadataJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got["callNumber"] != "QA76" {
		t.Errorf("unknown field lost: %v", got)
	}
	if got["number"] != float64(3) {
		t.Errorf("unknown numeric field lost: %v", got)
	}
	if got["title"] != "Normalized" {
		t.Errorf("known field not normalized: %v", got["title"])
	}
	if got["key"] != "ABCD1234" || got["version"] != float64(7) || got["itemType"] != "journalArticle" {
		t.Errorf("identity = %v", got)
	}
	if got["date"] != "2020" || got["collections"].([]any)[0] != "COLL1" {
		t.Errorf("fields = %v", got)
	}
	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 1 {
		t.Fatalf("tags = %v", got["tags"])
	}
	tag := tags[0].(map[string]any)
	if tag["tag"] != "x" || tag["type"] != float64(1) {
		t.Errorf("tags = %v", tag)
	}
	for _, forbidden := range []string{"deleted", "libraryID", "Raw", "year", "authors"} {
		if _, ok := got[forbidden]; ok {
			t.Errorf("internal/derived key %q leaked into metadata", forbidden)
		}
	}
	if body == "" || body[0] != '{' {
		t.Errorf("not an object: %s", body)
	}
}

func TestMetadataJSONMarshalError(t *testing.T) {
	item := literature.ZoteroItem{Raw: map[string]any{"bad": func() {}}}
	if _, err := MetadataJSON(item); err == nil {
		t.Fatal("expected marshal error")
	}
}
