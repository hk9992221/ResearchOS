package zotero

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/literature"
)

type stubReader struct {
	source literature.MetadataSource
	err    error
	page   literature.ItemPage
	item   literature.ZoteroItem
	calls  int
}

func (s *stubReader) Source() literature.MetadataSource { return s.source }

func (s *stubReader) step() error {
	s.calls++
	return s.err
}

func (s *stubReader) ListLibraries(ctx context.Context) ([]literature.ZoteroLibrary, error) {
	if err := s.step(); err != nil {
		return nil, err
	}
	return []literature.ZoteroLibrary{{ID: "0", Name: "stub"}}, nil
}

func (s *stubReader) ListCollections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error) {
	if err := s.step(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *stubReader) ListItems(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	if err := s.step(); err != nil {
		return literature.ItemPage{}, err
	}
	page := s.page
	page.SourceUsed = s.source
	return page, nil
}

func (s *stubReader) GetItem(ctx context.Context, libraryID, key string) (literature.ZoteroItem, error) {
	if err := s.step(); err != nil {
		return literature.ZoteroItem{}, err
	}
	return s.item, nil
}

func (s *stubReader) ListAttachments(ctx context.Context, libraryID, parentKey string) ([]literature.ZoteroAttachment, error) {
	if err := s.step(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *stubReader) ResolveAttachment(ctx context.Context, att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	if err := s.step(); err != nil {
		return literature.ResolvedFile{}, err
	}
	return literature.ResolvedFile{Path: att.Path}, nil
}

func TestSourceFallsBackPerCall(t *testing.T) {
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)
	api := &stubReader{source: literature.SourceLocalAPI, err: errors.New("api 爆炸")}
	src := NewSource(nil, sqlite, api)
	ctx := context.Background()

	reader, err := src.Reader(ctx)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	page, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListItems fallback: %v", err)
	}
	if page.SourceUsed != literature.SourceSQLite || page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("page = %+v", page)
	}
	if api.calls != 1 {
		t.Errorf("api calls = %d", api.calls)
	}

	mode, reason, err := src.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if mode != literature.SourceSQLite {
		t.Errorf("mode = %q", mode)
	}
	if !strings.Contains(reason, "api 爆炸") {
		t.Errorf("reason = %q", reason)
	}

	item, err := reader.GetItem(ctx, "0", "ITEM0001")
	if err != nil {
		t.Fatalf("GetItem fallback: %v", err)
	}
	if item.Key != "ITEM0001" || item.Title != "深度学习在气候预测中的应用" {
		t.Errorf("item = %+v", item)
	}
	if api.calls != 2 {
		t.Errorf("api calls = %d, want 2", api.calls)
	}
}

func TestSourceRecoversAfterAPISuccess(t *testing.T) {
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)
	api := &stubReader{source: literature.SourceLocalAPI, err: errors.New("temporary")}
	src := NewSource(nil, sqlite, api)
	ctx := context.Background()

	reader, err := src.Reader(ctx)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if _, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 1}); err != nil {
		t.Fatalf("fallback list: %v", err)
	}
	if mode, _, _ := src.Status(ctx); mode != literature.SourceSQLite {
		t.Fatalf("mode after fallback = %q", mode)
	}

	api.err = nil
	api.page = literature.ItemPage{Total: 1, Items: []literature.ZoteroItem{{Key: "API00001", Title: "API"}}}
	page, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 1})
	if err != nil {
		t.Fatalf("api list: %v", err)
	}
	if page.SourceUsed != literature.SourceLocalAPI || page.Items[0].Key != "API00001" {
		t.Errorf("page = %+v", page)
	}
	mode, reason, err := src.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if mode != literature.SourceLocalAPI || reason != "" {
		t.Errorf("mode=%q reason=%q", mode, reason)
	}
}

func TestSourceAPIUnavailableUsesSQLite(t *testing.T) {
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)

	refused := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	refusedURL := refused.URL
	refused.Close()
	conn := NewConnection(Options{
		GOOS:            "linux",
		HomeDir:         filepath.Join(t.TempDir(), "home"),
		LocalAPIBaseURL: refusedURL,
	})
	api := &stubReader{source: literature.SourceLocalAPI}
	src := NewSource(conn, sqlite, api)
	ctx := context.Background()

	reader, err := src.Reader(ctx)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if reader != sqlite {
		t.Errorf("reader = %T, want sqlite", reader)
	}
	if api.calls != 0 {
		t.Errorf("api was called despite unavailable probe")
	}
	mode, reason, err := src.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if mode != literature.SourceSQLite || !strings.Contains(reason, hintZoteroClosed) {
		t.Errorf("mode=%q reason=%q", mode, reason)
	}
}

func TestSourceProbeAvailableWrapsAPI(t *testing.T) {
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []any{}, 0)
	}))
	defer server.Close()
	conn := NewConnection(Options{
		GOOS:            "linux",
		HomeDir:         filepath.Join(t.TempDir(), "home"),
		LocalAPIBaseURL: server.URL,
	})
	api := &stubReader{
		source: literature.SourceLocalAPI,
		page:   literature.ItemPage{Total: 1, Items: []literature.ZoteroItem{{Key: "API00001"}}},
	}
	src := NewSource(conn, sqlite, api)
	reader, err := src.Reader(context.Background())
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	page, err := reader.ListItems(context.Background(), "0", "", literature.ListItemsOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if page.SourceUsed != literature.SourceLocalAPI || api.calls != 1 {
		t.Errorf("page=%+v calls=%d", page, api.calls)
	}
}

func TestSourceBothUnavailable(t *testing.T) {
	refused := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	refusedURL := refused.URL
	refused.Close()
	conn := NewConnection(Options{
		GOOS:            "linux",
		HomeDir:         filepath.Join(t.TempDir(), "home"),
		LocalAPIBaseURL: refusedURL,
	})
	src := NewSource(conn, nil, &stubReader{source: literature.SourceLocalAPI})
	if _, err := src.Reader(context.Background()); !errors.Is(err, literature.ErrZoteroUnavailable) {
		t.Errorf("Reader error = %v", err)
	}
	if _, _, err := src.Status(context.Background()); !errors.Is(err, literature.ErrZoteroUnavailable) {
		t.Errorf("Status error = %v", err)
	}

	empty := NewSource(nil, nil, nil)
	if _, err := empty.Reader(context.Background()); !errors.Is(err, literature.ErrZoteroUnavailable) {
		t.Errorf("empty Reader error = %v", err)
	}
	if _, _, err := empty.Status(context.Background()); !errors.Is(err, literature.ErrZoteroUnavailable) {
		t.Errorf("empty Status error = %v", err)
	}
}

func TestSourceSQLiteOnly(t *testing.T) {
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)
	src := NewSource(nil, sqlite, nil)
	reader, err := src.Reader(context.Background())
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if reader != sqlite {
		t.Errorf("reader = %T", reader)
	}
	mode, reason, err := src.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if mode != literature.SourceSQLite || !strings.Contains(reason, "本地 API 未配置") {
		t.Errorf("mode=%q reason=%q", mode, reason)
	}
}

func TestSourceSharesSQLiterResolver(t *testing.T) {
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)
	api := NewLocalAPIReader("http://127.0.0.1:1", nil)
	NewSource(nil, sqlite, api)
	if api.Resolver().DataDir != sqlite.DataDir() {
		t.Errorf("resolver data dir = %q", api.Resolver().DataDir)
	}
	if api.Resolver().LinkedBaseDir != f.LinkedDir {
		t.Errorf("resolver linked base = %q", api.Resolver().LinkedBaseDir)
	}
}

func TestSourceAPIOnlyFailureIsUnavailable(t *testing.T) {
	api := &stubReader{source: literature.SourceLocalAPI, err: errors.New("api down")}
	src := NewSource(nil, nil, api)
	ctx := context.Background()
	reader, err := src.Reader(ctx)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if _, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{}); err == nil {
		t.Fatal("expected API error")
	}
	mode, reason, err := src.Status(ctx)
	if !errors.Is(err, literature.ErrZoteroUnavailable) || mode != "" {
		t.Fatalf("Status mode=%q err=%v", mode, err)
	}
	if !strings.Contains(reason, "api down") {
		t.Errorf("reason = %q", reason)
	}

	// A benign "not found" answer does not make the source unavailable.
	api.err = literature.ErrItemNotFound
	if _, err := reader.GetItem(ctx, "0", "MISSING"); !errors.Is(err, literature.ErrItemNotFound) {
		t.Fatalf("GetItem error = %v", err)
	}
	mode, reason, err = src.Status(ctx)
	if err != nil || mode != literature.SourceLocalAPI || reason != "" {
		t.Errorf("Status after not-found mode=%q reason=%q err=%v", mode, reason, err)
	}
}
