package zotero

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"researchos/internal/literature"
)

type recordedRequest struct {
	Path  string
	Query map[string]string
}

type apiTestServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func (s *apiTestServer) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}
	s.requests = append(s.requests, recordedRequest{Path: r.URL.Path, Query: query})
}

func (s *apiTestServer) requestsFor(path string) []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedRequest
	for _, req := range s.requests {
		if req.Path == path {
			out = append(out, req)
		}
	}
	return out
}

func newAPITestServer(t *testing.T, handler http.HandlerFunc) *apiTestServer {
	t.Helper()
	s := &apiTestServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request: %s %s", r.Method, r.URL)
		}
		s.record(r)
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func writeJSON(w http.ResponseWriter, status int, body any, total int) {
	if total >= 0 {
		w.Header().Set("Total-Results", strconv.Itoa(total))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func apiItem(key, itemType, title string) map[string]any {
	return map[string]any{
		"key":     key,
		"version": float64(42),
		"data": map[string]any{
			"key":              key,
			"version":          float64(42),
			"itemType":         itemType,
			"title":            title,
			"date":             "2024",
			"DOI":              "10.1/" + key,
			"url":              "https://example.org/" + key,
			"abstractNote":     "摘要",
			"publicationTitle": "Journal",
			"language":         "en",
			"extra":            "extra-" + key,
			"callNumber":       "QA76",
			"creators": []any{
				map[string]any{"firstName": "Ada", "lastName": "Lovelace", "creatorType": "author"},
				map[string]any{"name": "某研究所", "creatorType": "author"},
			},
			"tags":        []any{map[string]any{"tag": "climate", "type": float64(1)}},
			"collections": []any{"COLLAAAA"},
		},
	}
}

func TestLocalAPIReaderListItems(t *testing.T) {
	server := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users/0/items/top":
			writeJSON(w, http.StatusOK, []any{
				apiItem("ITEM0001", "journalArticle", "深度学习"),
				apiItem("ITEM0002", "book", "Boring Systems"),
			}, 5)
		case "/api/users/0/collections/COLLAAAA/items/top":
			writeJSON(w, http.StatusOK, []any{apiItem("ITEM0003", "journalArticle", "Third")}, 1)
		default:
			http.NotFound(w, r)
		}
	})

	reader := NewLocalAPIReader(server.URL, server.Client())
	if reader.Source() != literature.SourceLocalAPI {
		t.Fatalf("Source = %q", reader.Source())
	}
	page, err := reader.ListItems(context.Background(), "0", "", literature.ListItemsOptions{Offset: 1, Limit: 2, Query: "climate"})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if page.Total != 5 || len(page.Items) != 2 || page.SourceUsed != literature.SourceLocalAPI {
		t.Fatalf("page = total %d source %q items %+v", page.Total, page.SourceUsed, page.Items)
	}
	item := page.Items[0]
	if item.Key != "ITEM0001" || item.Title != "深度学习" || item.ItemType != "journalArticle" || item.Version != 42 {
		t.Errorf("item = %+v", item)
	}
	if len(item.Creators) != 2 || item.Creators[0].LastName != "Lovelace" || item.Creators[1].Name != "某研究所" {
		t.Errorf("creators = %+v", item.Creators)
	}
	if len(item.Tags) != 1 || item.Tags[0] != "climate" {
		t.Errorf("tags = %+v", item.Tags)
	}
	if len(item.Collections) != 1 || item.Collections[0] != "COLLAAAA" {
		t.Errorf("collections = %+v", item.Collections)
	}
	if item.Raw["callNumber"] != "QA76" {
		t.Errorf("raw unknown field lost: %+v", item.Raw)
	}
	meta := ToPaperMetadata(item)
	if meta.Year != 2024 || meta.Authors != "Lovelace, Ada, 某研究所" {
		t.Errorf("metadata derived = year %d authors %q", meta.Year, meta.Authors)
	}
	if len(meta.Tags) != 1 || meta.Tags[0].Type != 1 {
		t.Errorf("tag type = %+v", meta.Tags)
	}

	reqs := server.requestsFor("/api/users/0/items/top")
	if len(reqs) != 1 {
		t.Fatalf("item requests = %+v", reqs)
	}
	if reqs[0].Query["start"] != "1" || reqs[0].Query["limit"] != "2" || reqs[0].Query["q"] != "climate" {
		t.Errorf("query = %+v", reqs[0].Query)
	}

	if _, err := reader.ListItems(context.Background(), "0", "", literature.ListItemsOptions{Limit: 500}); err != nil {
		t.Fatalf("clamped ListItems: %v", err)
	}
	reqs = server.requestsFor("/api/users/0/items/top")
	if got := reqs[len(reqs)-1].Query["limit"]; got != "100" {
		t.Errorf("limit = %s, want 100", got)
	}

	collection, err := reader.ListItems(context.Background(), "0", "COLLAAAA", literature.ListItemsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("collection ListItems: %v", err)
	}
	if collection.Total != 1 || len(collection.Items) != 1 || collection.Items[0].Key != "ITEM0003" {
		t.Errorf("collection page = %+v", collection)
	}
	if len(server.requestsFor("/api/users/0/collections/COLLAAAA/items/top")) != 1 {
		t.Errorf("collection endpoint not used")
	}
}

func TestLocalAPIReaderCollectionsPagination(t *testing.T) {
	const count = 101
	server := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/0/collections" {
			http.NotFound(w, r)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var out []any
		for i := start; i < count && i < start+limit; i++ {
			parent := any(false)
			if i%2 == 1 {
				parent = "PARENT"
			}
			out = append(out, map[string]any{
				"key": fmt.Sprintf("COLL%04d", i),
				"data": map[string]any{
					"key":              fmt.Sprintf("COLL%04d", i),
					"name":             fmt.Sprintf("Collection %d", i),
					"parentCollection": parent,
				},
				"meta": map[string]any{"numItems": float64(i)},
			})
		}
		writeJSON(w, http.StatusOK, out, count)
	})

	reader := NewLocalAPIReader(server.URL, server.Client())
	collections, err := reader.ListCollections(context.Background(), "0")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(collections) != count {
		t.Fatalf("len = %d, want %d", len(collections), count)
	}
	if collections[0].Key != "COLL0000" || collections[0].Name != "Collection 0" ||
		collections[0].ParentKey != "" || collections[0].ItemCount != 0 {
		t.Errorf("first = %+v", collections[0])
	}
	if collections[1].ParentKey != "PARENT" || collections[1].ItemCount != 1 {
		t.Errorf("second = %+v", collections[1])
	}
	reqs := server.requestsFor("/api/users/0/collections")
	if len(reqs) != 2 {
		t.Fatalf("collection requests = %+v", reqs)
	}
	if reqs[0].Query["start"] != "0" || reqs[0].Query["limit"] != "100" ||
		reqs[1].Query["start"] != "100" {
		t.Errorf("pagination = %+v", reqs)
	}
}

func TestLocalAPIReaderGetItemAndErrors(t *testing.T) {
	server := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users/0/items/ITEM0001":
			writeJSON(w, http.StatusOK, apiItem("ITEM0001", "journalArticle", "深度学习"), -1)
		default:
			http.NotFound(w, r)
		}
	})
	reader := NewLocalAPIReader(server.URL+"/api/users/0", server.Client())
	item, err := reader.GetItem(context.Background(), "0", "ITEM0001")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Key != "ITEM0001" || item.Title != "深度学习" {
		t.Errorf("item = %+v", item)
	}
	if _, err := reader.GetItem(context.Background(), "0", "MISSING"); !errors.Is(err, literature.ErrItemNotFound) {
		t.Errorf("missing error = %v", err)
	}
	if _, err := reader.ListItems(context.Background(), "1", "", literature.ListItemsOptions{}); err == nil {
		t.Error("group library should be rejected in v1")
	}
}

func TestLocalAPIReaderForbidden(t *testing.T) {
	server := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"}, -1)
	})
	reader := NewLocalAPIReader(server.URL, server.Client())
	libs, err := reader.ListLibraries(context.Background())
	if err != nil || len(libs) != 1 {
		t.Fatalf("ListLibraries = %+v, %v", libs, err)
	}
	_, err = reader.ListItems(context.Background(), "0", "", literature.ListItemsOptions{})
	if !errors.Is(err, literature.ErrLocalAPIDisabled) {
		t.Fatalf("error = %v, want ErrLocalAPIDisabled", err)
	}
}

func TestLocalAPIReaderAttachmentsAndResolve(t *testing.T) {
	server := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/0/items/ITEM0001/children" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, []any{
			map[string]any{
				"key": "ATT00001",
				"data": map[string]any{
					"key":         "ATT00001",
					"itemType":    "attachment",
					"parentItem":  "ITEM0001",
					"contentType": "application/pdf",
					"filename":    "paper-one.pdf",
					"path":        "storage:paper-one.pdf",
					"linkMode":    "imported_file",
				},
			},
			map[string]any{
				"key": "ATT00002",
				"data": map[string]any{
					"key":         "ATT00002",
					"itemType":    "attachment",
					"parentItem":  "ITEM0001",
					"contentType": "text/html",
					"path":        "storage:index.html",
					"linkMode":    "imported_file",
				},
			},
			map[string]any{
				"key": "NOTE0001",
				"data": map[string]any{
					"key":      "NOTE0001",
					"itemType": "note",
				},
			},
			map[string]any{
				"key": "ATT00003",
				"data": map[string]any{
					"key":         "ATT00003",
					"itemType":    "attachment",
					"parentItem":  "ITEM0001",
					"filename":    "no-content-type.PDF",
					"linkMode":    "linked_file",
					"path":        "attachments:paper.pdf",
					"contentType": "",
				},
			},
		}, 4)
	})

	reader := NewLocalAPIReader(server.URL, server.Client())
	atts, err := reader.ListAttachments(context.Background(), "0", "ITEM0001")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("attachments = %+v", atts)
	}
	if atts[0].Key != "ATT00001" || atts[0].ParentKey != "ITEM0001" || atts[0].Filename != "paper-one.pdf" ||
		atts[0].LinkMode != "imported_file" || atts[0].ContentType != "application/pdf" {
		t.Errorf("attachment 0 = %+v", atts[0])
	}
	if atts[1].Key != "ATT00003" || atts[1].LinkMode != "linked_file" {
		t.Errorf("attachment 1 = %+v", atts[1])
	}

	file := filepath.Join(t.TempDir(), "absolute.pdf")
	if err := os.WriteFile(file, []byte("%PDF"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	abs := literature.ZoteroAttachment{Key: "ABS", Path: file, LinkMode: "linked_file"}
	resolved, err := reader.ResolveAttachment(context.Background(), abs)
	if err != nil {
		t.Fatalf("ResolveAttachment: %v", err)
	}
	if resolved.Path != file || resolved.Size != 4 {
		t.Errorf("resolved = %+v", resolved)
	}

	storage := literature.ZoteroAttachment{Key: "ATT00001", Path: "storage:paper.pdf", LinkMode: "imported_file"}
	if _, err := reader.ResolveAttachment(context.Background(), storage); !errors.Is(err, literature.ErrAttachmentUnresolved) {
		t.Errorf("unconfigured storage error = %v", err)
	}
}

func TestLocalAPIReaderAcceptsFullBase(t *testing.T) {
	server := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/0/collections" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, []any{}, 0)
	})
	reader := NewLocalAPIReader(server.URL+"/api/users/0/", server.Client())
	if _, err := reader.ListCollections(context.Background(), "0"); err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
}
