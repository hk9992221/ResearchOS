package zotero

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/literature"
)

// newFakeZoteroAPI serves the three endpoints the local API reader touches and
// records the query of the last item request.
func newFakeZoteroAPI(t *testing.T) (*httptest.Server, *url.Values) {
	t.Helper()
	lastQuery := &url.Values{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/0/collections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Total-Results", "0")
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/users/0/items/top", func(w http.ResponseWriter, r *http.Request) {
		*lastQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Total-Results", "1")
		_, _ = w.Write([]byte(`[{"key":"ITEM0001","version":7,"data":{` +
			`"key":"ITEM0001","itemType":"journalArticle","title":"Vibration of plates"}}]`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, lastQuery
}

func newTestConnection(baseURL string) *Connection {
	return NewConnection(Options{LocalAPIBaseURL: baseURL})
}

func TestNewAPIReaderNilReceiver(t *testing.T) {
	var c *Connection
	if reader := c.NewAPIReader(); reader != nil {
		t.Fatalf("nil receiver returned %v, want nil", reader)
	}
}

func TestNewAPIReaderUsesUsersEndpoint(t *testing.T) {
	srv, _ := newFakeZoteroAPI(t)
	reader := newTestConnection(srv.URL).NewAPIReader()
	if reader == nil {
		t.Fatal("NewAPIReader returned nil")
	}
	want := srv.URL + "/api/users/0"
	if reader.base != want {
		t.Fatalf("base = %q, want %q", reader.base, want)
	}
	if got := reader.Source(); got != literature.SourceLocalAPI {
		t.Fatalf("Source() = %q, want %q", got, literature.SourceLocalAPI)
	}
}

// TestSourceServesFromLocalAPI is the regression test for the boot wiring: a
// Source built with the connection's API reader must answer from the API even
// when no SQLite reader is available.
func TestSourceServesFromLocalAPI(t *testing.T) {
	srv, lastQuery := newFakeZoteroAPI(t)
	conn := newTestConnection(srv.URL)
	source := NewSource(conn, nil, conn.NewAPIReader())

	ctx := context.Background()
	reader, err := source.Reader(ctx)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if got := reader.Source(); got != literature.SourceLocalAPI {
		t.Fatalf("reader.Source() = %q, want %q", got, literature.SourceLocalAPI)
	}

	page, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 100, Query: "vibration"})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(page.Items))
	}
	if page.Items[0].Key != "ITEM0001" || page.Items[0].Title != "Vibration of plates" {
		t.Fatalf("unexpected item: %+v", page.Items[0])
	}
	if page.Total != 1 {
		t.Fatalf("Total = %d, want 1", page.Total)
	}
	if page.SourceUsed != literature.SourceLocalAPI {
		t.Fatalf("SourceUsed = %q, want %q", page.SourceUsed, literature.SourceLocalAPI)
	}
	if got := lastQuery.Get("q"); got != "vibration" {
		t.Fatalf("query q = %q, want %q", got, "vibration")
	}

	res, reason, err := source.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if res != literature.SourceLocalAPI {
		t.Fatalf("Status source = %q, want %q", res, literature.SourceLocalAPI)
	}
	if reason != "" {
		t.Fatalf("Status reason = %q, want empty", reason)
	}
}

// TestSourceFallsBackToSQLiteWhenAPIDown covers the other half of the wiring:
// the API reader is not forced, so a closed Zotero process still yields the
// SQLite reader when a database is configured.
func TestSourceFallsBackToSQLiteWhenAPIDown(t *testing.T) {
	srv, _ := newFakeZoteroAPI(t)
	baseURL := srv.URL
	srv.Close()

	dir := t.TempDir()
	writeMinimalZoteroDB(t, filepath.Join(dir, "zotero.sqlite"))
	sqlite, err := NewSQLiteReader(dir)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })

	conn := newTestConnection(baseURL)
	source := NewSource(conn, sqlite, conn.NewAPIReader())

	ctx := context.Background()
	reader, err := source.Reader(ctx)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if got := reader.Source(); got != literature.SourceSQLite {
		t.Fatalf("reader.Source() = %q, want %q", got, literature.SourceSQLite)
	}
}

// TestStatusReportsProbeFailureWhenAPIDown checks the diagnostic path used by
// the removed settings card: the reason names the local API probe.
func TestStatusReportsProbeFailureWhenAPIDown(t *testing.T) {
	srv, _ := newFakeZoteroAPI(t)
	baseURL := srv.URL
	srv.Close()

	conn := newTestConnection(baseURL)
	source := NewSource(conn, nil, conn.NewAPIReader())

	res, reason, err := source.Status(context.Background())
	if !errors.Is(err, literature.ErrZoteroUnavailable) {
		t.Fatalf("Status error = %v, want ErrZoteroUnavailable", err)
	}
	if res != "" {
		t.Fatalf("Status source = %q, want empty", res)
	}
	if !strings.Contains(reason, hintZoteroClosed) {
		t.Fatalf("Status reason = %q, want it to mention %q", reason, hintZoteroClosed)
	}
}

// writeMinimalZoteroDB creates a valid, empty SQLite database at path so that
// NewSQLiteReader's open-and-ping succeeds.
func writeMinimalZoteroDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE version (schema TEXT)"); err != nil {
		t.Skipf("cannot create fixture database: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture database missing: %v", err)
	}
}
