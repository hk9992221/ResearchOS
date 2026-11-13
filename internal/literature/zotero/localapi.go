package zotero

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"researchos/internal/literature"
)

const (
	localAPIReadTimeout = 3 * time.Second
	localAPIPageLimit   = 100
)

var _ literature.ZoteroReader = (*LocalAPIReader)(nil)

// LocalAPIReader reads metadata from Zotero's local HTTP API. It only issues
// GET requests, paginates serially and never mutates Zotero state.
type LocalAPIReader struct {
	base     string
	hc       *http.Client
	resolver Resolver
}

// NewLocalAPIReader builds a reader for baseURL. The base may be the Zotero
// root ("http://127.0.0.1:23119") or an explicit user-library endpoint
// ("…/api/users/0"); the missing "/api/users/0" is appended. A nil client uses
// http.DefaultClient with a per-request 3s deadline.
func NewLocalAPIReader(baseURL string, hc *http.Client) *LocalAPIReader {
	return &LocalAPIReader{base: normalizeUsersBase(baseURL), hc: hc}
}

// WithResolver configures attachment path resolution. Call it before the
// reader is shared between goroutines.
func (r *LocalAPIReader) WithResolver(res Resolver) *LocalAPIReader {
	r.resolver = res
	return r
}

// Resolver returns the current attachment resolver.
func (r *LocalAPIReader) Resolver() Resolver { return r.resolver }

// Source implements literature.ZoteroReader.
func (r *LocalAPIReader) Source() literature.MetadataSource { return literature.SourceLocalAPI }

// normalizeUsersBase appends the Zotero 7 personal-library path when absent.
func normalizeUsersBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = DefaultLocalAPIBaseURL
	}
	lower := strings.ToLower(base)
	switch {
	case strings.Contains(lower, "/api/users/"):
		return base
	case strings.HasSuffix(lower, "/api/users"):
		return base + "/0"
	case strings.HasSuffix(lower, "/api"):
		return base + "/users/0"
	default:
		return base + "/api/users/0"
	}
}

// ListLibraries returns only the personal library (SPEC-04 §5.2, O-05).
func (r *LocalAPIReader) ListLibraries(ctx context.Context) ([]literature.ZoteroLibrary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []literature.ZoteroLibrary{{ID: "0", Name: "我的文库"}}, nil
}

// ListCollections returns all collections of the personal library, including
// nested ones (the caller builds the tree from ParentKey).
func (r *LocalAPIReader) ListCollections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error) {
	if err := checkLibrary(libraryID); err != nil {
		return nil, err
	}
	var out []literature.ZoteroCollection
	start := 0
	for range 1000 {
		var objects []apiObject
		header, err := r.getJSON(ctx, "/collections?"+pageQuery(start, localAPIPageLimit, ""), &objects)
		if err != nil {
			return nil, err
		}
		for _, obj := range objects {
			data := obj.Data
			if data == nil {
				continue
			}
			key := stringValue(data["key"])
			if key == "" {
				key = obj.Key
			}
			parent := ""
			if p, ok := data["parentCollection"].(string); ok {
				parent = p
			}
			out = append(out, literature.ZoteroCollection{
				Key:       key,
				ParentKey: parent,
				Name:      stringValue(data["name"]),
				ItemCount: intValue(obj.Meta["numItems"]),
			})
		}
		if len(objects) < localAPIPageLimit {
			break
		}
		start += len(objects)
		if total, ok := totalResults(header); ok && start >= total {
			break
		}
	}
	return out, nil
}

// ListItems returns one page of top-level bibliographic items.
func (r *LocalAPIReader) ListItems(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	if err := checkLibrary(libraryID); err != nil {
		return literature.ItemPage{}, err
	}
	libraryID = normalizeLibraryID(libraryID)
	limit := opts.Limit
	if limit <= 0 || limit > localAPIPageLimit {
		limit = localAPIPageLimit
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	path := "/items/top"
	if key := strings.TrimSpace(collectionKey); key != "" {
		path = "/collections/" + url.PathEscape(key) + "/items/top"
	}

	page := literature.ItemPage{SourceUsed: literature.SourceLocalAPI}
	var objects []apiObject
	header, err := r.getJSON(ctx, path+"?"+pageQuery(offset, limit, opts.Query), &objects)
	if err != nil {
		return literature.ItemPage{}, err
	}
	for _, obj := range objects {
		page.Items = append(page.Items, itemFromAPIData(obj.Data, obj.Key, obj.Version, libraryID))
	}
	if total, ok := totalResults(header); ok {
		page.Total = total
	} else {
		page.Total = offset + len(page.Items)
	}
	return page, nil
}

// GetItem returns one item by key.
func (r *LocalAPIReader) GetItem(ctx context.Context, libraryID, key string) (literature.ZoteroItem, error) {
	if err := checkLibrary(libraryID); err != nil {
		return literature.ZoteroItem{}, err
	}
	libraryID = normalizeLibraryID(libraryID)
	var obj apiObject
	if _, err := r.getJSON(ctx, "/items/"+url.PathEscape(key), &obj); err != nil {
		return literature.ZoteroItem{}, err
	}
	return itemFromAPIData(obj.Data, obj.Key, obj.Version, libraryID), nil
}

// ListAttachments returns PDF attachments of one item.
func (r *LocalAPIReader) ListAttachments(ctx context.Context, libraryID, parentKey string) ([]literature.ZoteroAttachment, error) {
	if err := checkLibrary(libraryID); err != nil {
		return nil, err
	}
	libraryID = normalizeLibraryID(libraryID)
	var out []literature.ZoteroAttachment
	start := 0
	for range 1000 {
		var objects []apiObject
		header, err := r.getJSON(ctx, "/items/"+url.PathEscape(parentKey)+"/children?"+pageQuery(start, localAPIPageLimit, ""), &objects)
		if err != nil {
			return nil, err
		}
		for _, obj := range objects {
			data := obj.Data
			if stringValue(data["itemType"]) != "attachment" {
				continue
			}
			filename := stringValue(data["filename"])
			if filename == "" {
				// A linked file carries its name in `title`, not `filename`.
				filename = stringValue(data["title"])
			}
			rawPath := strings.TrimSpace(stringValue(data["path"]))
			if rawPath == "" {
				// Zotero 7 leaves `path` empty for storage copies and linked
				// files alike: the local location lives in the object's root
				// `links` (enclosure / linkedFile / importedFile), not in data.
				rawPath = attachmentHref(obj.Links)
			}
			contentType := stringValue(data["contentType"])
			if !isPDFAttachment(contentType, filename, rawPath) {
				continue
			}
			key := stringValue(data["key"])
			if key == "" {
				key = obj.Key
			}
			parent := stringValue(data["parentItem"])
			if parent == "" {
				parent = parentKey
			}
			att := literature.ZoteroAttachment{
				Key:         key,
				ParentKey:   parent,
				LibraryID:   libraryID,
				Filename:    filename,
				ContentType: contentType,
				LinkMode:    linkModeName(data["linkMode"]),
				Path:        rawPath,
				Size:        int64(intValue(data["size"])),
			}
			if resolved, err := r.resolver.Resolve(att); err == nil {
				att.Size = resolved.Size
			}
			out = append(out, att)
		}
		if len(objects) < localAPIPageLimit {
			break
		}
		start += len(objects)
		if total, ok := totalResults(header); ok && start >= total {
			break
		}
	}
	return out, nil
}

// ResolveAttachment maps an attachment record to a readable local file.
func (r *LocalAPIReader) ResolveAttachment(ctx context.Context, att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	if err := ctx.Err(); err != nil {
		return literature.ResolvedFile{}, err
	}
	return r.resolver.Resolve(att)
}

type apiObject struct {
	Key     string         `json:"key"`
	Version int            `json:"version"`
	Data    map[string]any `json:"data"`
	Links   map[string]any `json:"links"`
	Meta    map[string]any `json:"meta"`
}

func (r *LocalAPIReader) getJSON(ctx context.Context, pathAndQuery string, dst any) (http.Header, error) {
	rctx, cancel := context.WithTimeout(ctx, localAPIReadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, r.base+pathAndQuery, nil)
	if err != nil {
		return nil, fmt.Errorf("zotero: 构造本地 API 请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	hc := r.hc
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", literature.ErrZoteroUnavailable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return nil, fmt.Errorf("%w：%s", literature.ErrLocalAPIDisabled, hintAPIAllowApps)
	case http.StatusNotFound:
		return nil, literature.ErrItemNotFound
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("zotero: 本地 API 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if dst == nil {
		return resp.Header, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return nil, fmt.Errorf("zotero: 解析本地 API 响应: %w", err)
	}
	return resp.Header, nil
}

func pageQuery(start, limit int, q string) string {
	values := url.Values{}
	values.Set("start", strconv.Itoa(start))
	values.Set("limit", strconv.Itoa(limit))
	if strings.TrimSpace(q) != "" {
		values.Set("q", q)
	}
	return values.Encode()
}

func totalResults(header http.Header) (int, bool) {
	raw := strings.TrimSpace(header.Get("Total-Results"))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func normalizeLibraryID(libraryID string) string {
	if strings.TrimSpace(libraryID) == "" {
		return "0"
	}
	return strings.TrimSpace(libraryID)
}

func checkLibrary(libraryID string) error {
	switch strings.TrimSpace(libraryID) {
	case "", "0":
		return nil
	default:
		return fmt.Errorf("zotero: v1 仅支持个人库（%s）", libraryID)
	}
}

func itemFromAPIData(data map[string]any, fallbackKey string, fallbackVersion int, libraryID string) literature.ZoteroItem {
	if data == nil {
		data = map[string]any{}
	}
	key := stringValue(data["key"])
	if key == "" {
		key = fallbackKey
	}
	version := intValue(data["version"])
	if version == 0 {
		version = fallbackVersion
	}
	raw := make(map[string]any, len(data))
	for k, v := range data {
		raw[k] = v
	}
	return literature.ZoteroItem{
		Key:              key,
		LibraryID:        libraryID,
		Version:          version,
		ItemType:         stringValue(data["itemType"]),
		Title:            stringValue(data["title"]),
		Creators:         creatorsFromData(data["creators"]),
		Date:             stringValue(data["date"]),
		DOI:              stringValue(data["DOI"]),
		URL:              stringValue(data["url"]),
		AbstractNote:     stringValue(data["abstractNote"]),
		PublicationTitle: stringValue(data["publicationTitle"]),
		Tags:             tagNames(data["tags"]),
		Collections:      stringSlice(data["collections"]),
		Language:         stringValue(data["language"]),
		Extra:            stringValue(data["extra"]),
		Deleted:          boolValue(data["deleted"]),
		Raw:              raw,
	}
}

func creatorsFromData(v any) []literature.ZoteroCreator {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]literature.ZoteroCreator, 0, len(list))
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, literature.ZoteroCreator{
			FirstName:   stringValue(m["firstName"]),
			LastName:    stringValue(m["lastName"]),
			Name:        stringValue(m["name"]),
			CreatorType: stringValue(m["creatorType"]),
		})
	}
	return out
}

func tagNames(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		switch t := entry.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			if name := stringValue(t["tag"]); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func stringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if s, ok := entry.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func linkModeName(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	switch intValue(v) {
	case 0:
		return "imported_file"
	case 1:
		return "imported_url"
	case 2:
		return "linked_file"
	case 3:
		return "linked_url"
	default:
		return ""
	}
}

func isPDFAttachment(contentType, filename, path string) bool {
	if strings.EqualFold(strings.TrimSpace(contentType), "application/pdf") {
		return true
	}
	for _, candidate := range []string{filename, path} {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".pdf") {
			return true
		}
	}
	return false
}

// attachmentHref recovers the local location of an attachment whose `path`
// field is empty. The links map is the API object's root `links` (a sibling
// of `data`): Zotero 7 exposes the file as `enclosure.href` (a file:// URI
// into storage/ or the linked location); older shapes use `linkedFile` /
// `importedFile`.
func attachmentHref(links map[string]any) string {
	if links == nil {
		return ""
	}
	for _, name := range []string{"enclosure", "linkedFile", "importedFile"} {
		link, _ := links[name].(map[string]any)
		if link == nil {
			continue
		}
		if href := strings.TrimSpace(stringValue(link["href"])); href != "" {
			return localPathFromHref(href)
		}
	}
	return ""
}

// localPathFromHref turns a file:// URI into a readable filesystem path.
// Storage references pass through for the resolver's `storage:` rule; a URI
// that is not a plain local file passes through unchanged, so the resolver
// names the real href in its error.
func localPathFromHref(href string) string {
	if !strings.HasPrefix(strings.ToLower(href), "file:") {
		return href
	}
	u, err := url.Parse(href)
	if err != nil || u.Path == "" || (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) {
		return href
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil {
		p = u.Path
	}
	// file:///F:/papers/x.pdf decodes to "/F:/papers/x.pdf": the leading
	// slash before a drive letter is an artifact of the URI form.
	if len(p) >= 3 && p[0] == '/' && isDriveLetter(p[1]) && p[2] == ':' {
		p = p[1:]
	}
	return p
}

func isDriveLetter(b byte) bool {
	return ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}
