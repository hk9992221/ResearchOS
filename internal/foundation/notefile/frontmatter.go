package notefile

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"researchos/internal/foundation/clock"
)

// Meta is the YAML frontmatter carried by a note file. It mirrors the metadata
// row of the note; project.db stays the query authority, so a rewrite always
// takes the typed fields from the row and only preserves unknown keys from the
// file (see Merge).
type Meta struct {
	ID          string
	Kind        string
	Title       string
	Description string
	Tags        []string
	Created     time.Time
	Updated     time.Time

	// extras keeps unknown frontmatter keys verbatim, in a YAML mapping node,
	// so rewriting a file never drops a key the user added. Nil when the file
	// has none.
	extras *yaml.Node
}

// fence delimits the YAML frontmatter block.
const fence = "---"

// The typed frontmatter keys, in their fixed output order.
const (
	keyID          = "id"
	keyKind        = "kind"
	keyTitle       = "title"
	keyDescription = "description"
	keyTags        = "tags"
	keyCreated     = "created"
	keyUpdated     = "updated"
)

// Split separates the YAML frontmatter from the Markdown body. A file without
// frontmatter stays valid: it returns the zero Meta and the whole data as the
// body. A block that never closes, and malformed YAML, are treated the same
// way, so a body may legitimately start with "---". The body is a subslice of
// data, never a copy.
func Split(data []byte) (Meta, []byte) {
	block, body, ok := splitFences(data)
	if !ok {
		return Meta{}, data
	}
	var meta Meta
	if len(bytes.TrimSpace(block)) == 0 {
		return meta, body
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(block, &doc); err != nil || len(doc.Content) == 0 {
		return Meta{}, data
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return Meta{}, data
	}
	extras := yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		switch key.Value {
		case keyID:
			meta.ID = scalarValue(value)
		case keyKind:
			meta.Kind = scalarValue(value)
		case keyTitle:
			meta.Title = scalarValue(value)
		case keyDescription:
			meta.Description = scalarValue(value)
		case keyTags:
			if err := value.Decode(&meta.Tags); err == nil {
				continue
			}
			extras.Content = append(extras.Content, key, value)
		case keyCreated:
			meta.Created = timestampValue(value)
		case keyUpdated:
			meta.Updated = timestampValue(value)
		default:
			extras.Content = append(extras.Content, key, value)
		}
	}
	if len(extras.Content) > 0 {
		meta.extras = &extras
	}
	return meta, body
}

// Join renders meta as YAML frontmatter followed by body. Typed keys keep a
// fixed order and empty optional keys are omitted; unknown keys follow, sorted
// by name. A zero Meta yields body unchanged, so legacy files stay untouched
// unless there is something to write.
func Join(m Meta, body []byte) []byte {
	mapping := m.mapping()
	if mapping == nil {
		return body
	}
	block, err := yaml.Marshal(mapping)
	if err != nil {
		// Every node is a plain scalar or sequence of scalars; yaml.Marshal
		// cannot fail on them. Keep the body rather than corrupt the file.
		return body
	}
	out := make([]byte, 0, len(block)+len(body)+2*len(fence)+2)
	out = append(out, fence...)
	out = append(out, '\n')
	out = append(out, block...)
	out = append(out, fence...)
	out = append(out, '\n')
	out = append(out, body...)
	return out
}

// Merge returns base with the unknown keys of disk preserved. Typed fields come
// from base only: the database is the metadata authority.
func Merge(base, disk Meta) Meta {
	base.extras = disk.extras
	return base
}

// HasFrontmatter reports whether data starts with a closed frontmatter block.
func HasFrontmatter(data []byte) bool {
	_, _, ok := splitFences(data)
	return ok
}

// mapping renders m as a YAML mapping node, or nil when there is nothing to
// write.
func (m Meta) mapping() *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	put := func(key, value string) {
		if value == "" {
			return
		}
		root.Content = append(root.Content, stringNode(key), stringNode(value))
	}
	put(keyID, m.ID)
	put(keyKind, m.Kind)
	put(keyTitle, m.Title)
	put(keyDescription, m.Description)
	if len(m.Tags) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, tag := range m.Tags {
			seq.Content = append(seq.Content, stringNode(tag))
		}
		root.Content = append(root.Content, stringNode(keyTags), seq)
	}
	if !m.Created.IsZero() {
		put(keyCreated, clock.Format(m.Created))
	}
	if !m.Updated.IsZero() {
		put(keyUpdated, clock.Format(m.Updated))
	}
	if m.extras != nil && len(m.extras.Content) > 0 {
		type pair struct{ key, value *yaml.Node }
		pairs := make([]pair, 0, len(m.extras.Content)/2)
		for i := 0; i+1 < len(m.extras.Content); i += 2 {
			pairs = append(pairs, pair{m.extras.Content[i], m.extras.Content[i+1]})
		}
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].key.Value < pairs[j].key.Value })
		for _, p := range pairs {
			root.Content = append(root.Content, p.key, p.value)
		}
	}
	if len(root.Content) == 0 {
		return nil
	}
	return root
}

// splitFences cuts the leading frontmatter block off data. ok is false when the
// file does not start with a fence or the block never closes.
func splitFences(data []byte) (block, body []byte, ok bool) {
	line, rest, ok := nextLine(data)
	if !ok || !isFence(line) {
		return nil, nil, false
	}
	remaining := rest
	for {
		line, next, ok := nextLine(remaining)
		if !ok {
			return nil, nil, false
		}
		if isFence(line) {
			return rest[:len(rest)-len(remaining)], next, true
		}
		remaining = next
	}
}

// nextLine splits the first line off data, terminator included in neither
// return value. ok is false only for empty input.
func nextLine(data []byte) (line, rest []byte, ok bool) {
	if len(data) == 0 {
		return nil, nil, false
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return data[:i], data[i+1:], true
	}
	return data, nil, true
}

// isFence reports whether line is the frontmatter delimiter, tolerating CRLF.
func isFence(line []byte) bool {
	return strings.TrimSuffix(string(line), "\r") == fence
}

// scalarValue returns the text of a scalar node; any other node reads empty.
func scalarValue(node *yaml.Node) string {
	if node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

// timestampValue parses a frontmatter timestamp in the storage layout. An
// unparsable value reads as the zero time; the typed field is always rewritten
// from the row, so nothing is lost from the file's extras.
func timestampValue(node *yaml.Node) time.Time {
	at, err := clock.Parse(strings.TrimSpace(scalarValue(node)))
	if err != nil {
		return time.Time{}
	}
	return at
}

// stringNode is one plain YAML string scalar.
func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
