package zotero

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"researchos/internal/literature"
)

var fourDigits = regexp.MustCompile(`[0-9]{4}`)

// ToPaperMetadata maps a reader result onto the §4.3 metadata structure.
// Keys, versions and item types are copied verbatim; missing values stay empty
// and are never invented.
func ToPaperMetadata(item literature.ZoteroItem) literature.PaperMetadata {
	meta := literature.PaperMetadata{
		Key:              item.Key,
		Version:          item.Version,
		ItemType:         item.ItemType,
		Title:            item.Title,
		Creators:         append([]literature.ZoteroCreator(nil), item.Creators...),
		Date:             item.Date,
		PublicationTitle: item.PublicationTitle,
		DOI:              item.DOI,
		URL:              item.URL,
		AbstractNote:     item.AbstractNote,
		Tags:             normalizeTags(item.Tags, item.Raw),
		Collections:      append([]string(nil), item.Collections...),
		Language:         item.Language,
		Extra:            item.Extra,
	}
	meta.Year = deriveYear(meta.Date)
	meta.Authors = deriveAuthors(meta.Creators)
	return meta
}

// MetadataJSON returns compact JSON for papers.metadata. It starts from the
// reader's raw object (so unknown Zotero fields survive), overwrites every
// known field with the normalized value and drops internal-only keys. Derived
// fields (year/authors) are not written back (SPEC-04 §4.3).
func MetadataJSON(item literature.ZoteroItem) (string, error) {
	raw := make(map[string]any, len(item.Raw)+14)
	for k, v := range item.Raw {
		if internalMetadataKey(k) {
			continue
		}
		raw[k] = v
	}

	meta := ToPaperMetadata(item)
	raw["key"] = meta.Key
	raw["version"] = meta.Version
	raw["itemType"] = meta.ItemType
	raw["title"] = meta.Title
	raw["creators"] = meta.Creators
	raw["date"] = meta.Date
	raw["publicationTitle"] = meta.PublicationTitle
	raw["DOI"] = meta.DOI
	raw["url"] = meta.URL
	raw["abstractNote"] = meta.AbstractNote
	raw["tags"] = meta.Tags
	raw["collections"] = meta.Collections
	raw["language"] = meta.Language
	raw["extra"] = meta.Extra

	body, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func internalMetadataKey(key string) bool {
	switch key {
	case "Raw", "raw", "libraryID", "libraryId", "itemID", "deleted", "Deleted",
		"attachmentKey", "source", "sourceState", "fileState", "filePath":
		return true
	}
	return false
}

// deriveYear returns the first four-digit run in date, or 0.
func deriveYear(date string) int {
	m := fourDigits.FindString(date)
	if m == "" {
		return 0
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0
	}
	return n
}

// deriveAuthors renders creators in Zotero order as "Last, First" and joins
// them with ", ". Single-field creators use their name verbatim.
func deriveAuthors(creators []literature.ZoteroCreator) string {
	parts := make([]string, 0, len(creators))
	for _, c := range creators {
		switch {
		case strings.TrimSpace(c.LastName) != "":
			if first := strings.TrimSpace(c.FirstName); first != "" {
				parts = append(parts, strings.TrimSpace(c.LastName)+", "+first)
			} else {
				parts = append(parts, strings.TrimSpace(c.LastName))
			}
		case strings.TrimSpace(c.Name) != "":
			parts = append(parts, strings.TrimSpace(c.Name))
		case strings.TrimSpace(c.FirstName) != "":
			parts = append(parts, strings.TrimSpace(c.FirstName))
		}
	}
	return strings.Join(parts, ", ")
}

// normalizeTags turns the reader's string tags into {tag,type} objects. Tag
// types come from the raw Zotero tag objects when available; otherwise type 0
// (normal tag) is used.
func normalizeTags(tags []string, raw map[string]any) []literature.ZoteroTag {
	if len(tags) == 0 {
		return rawTags(raw)
	}
	types := rawTagTypes(raw)
	out := make([]literature.ZoteroTag, 0, len(tags))
	for _, name := range tags {
		out = append(out, literature.ZoteroTag{Tag: name, Type: types[name]})
	}
	return out
}

// rawTags reconstructs tag objects when the reader only kept the raw map.
func rawTags(raw map[string]any) []literature.ZoteroTag {
	values, ok := raw["tags"]
	if !ok {
		return nil
	}
	switch list := values.(type) {
	case []literature.ZoteroTag:
		out := make([]literature.ZoteroTag, len(list))
		copy(out, list)
		return out
	case []any:
		out := make([]literature.ZoteroTag, 0, len(list))
		for _, entry := range list {
			switch t := entry.(type) {
			case string:
				out = append(out, literature.ZoteroTag{Tag: t})
			case map[string]any:
				name, _ := t["tag"].(string)
				if name == "" {
					continue
				}
				out = append(out, literature.ZoteroTag{Tag: name, Type: intValue(t["type"])})
			}
		}
		return out
	case []map[string]any:
		out := make([]literature.ZoteroTag, 0, len(list))
		for _, t := range list {
			name, _ := t["tag"].(string)
			if name == "" {
				continue
			}
			out = append(out, literature.ZoteroTag{Tag: name, Type: intValue(t["type"])})
		}
		return out
	}
	return nil
}

func rawTagTypes(raw map[string]any) map[string]int {
	out := map[string]int{}
	for _, tag := range rawTags(raw) {
		out[tag.Tag] = tag.Type
	}
	return out
}

func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}
