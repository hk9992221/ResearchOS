package boot

// The Literature page reaches SPEC-04 through this adapter, the same way the
// other pages reach their module: the composition root builds the services,
// the page only calls narrow interfaces. Construction mirrors the MCP binding
// (internal/mcpbind/literature.go) so the two entry points behave identically.

import (
	"context"
	"errors"
	"sync"

	"researchos/internal/foundation/clock"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/literature"
	"researchos/internal/literature/library"
	"researchos/internal/literature/zotero"
	"researchos/internal/search"
	"researchos/internal/ui/pages"
)

// personalLibrary is the Zotero library the page offers: v1 is personal-only
// (SPEC-04 §5.2), so group libraries stay out of the browser.
const personalLibrary = "0"

// projectPaperLimit caps the list of papers already linked to the project.
// SPEC-04 allows at most 500 per query.
const projectPaperLimit = 500

// literatureModule owns the Zotero connection, its reader and the library
// service of one open workspace.
type literatureModule struct {
	ws    *fworkspace.Workspace
	clock clock.Clock
	conn  *zotero.Connection

	// mu guards the resolved reader: changing the Zotero directory in the
	// settings must not keep the page reading the previous database.
	mu     sync.Mutex
	dir    string
	sqlite *zotero.SQLiteReader
	source *zotero.Source
	lib    *library.Service
	search *search.Service
}

var _ pages.LiteratureAPI = (*literatureModule)(nil)

func newLiteratureModule(ws *fworkspace.Workspace, searchSvc *search.Service) *literatureModule {
	c := ws.Clock()
	if c == nil {
		c = clock.System{}
	}
	return &literatureModule{
		ws:     ws,
		clock:  c,
		conn:   zotero.NewConnection(zotero.Options{AppConfig: ws.Config(), Clock: c}),
		search: searchSvc,
	}
}

// ensure resolves the current Zotero data directory and rebuilds the reader
// when it changed. A directory that is missing or invalid leaves the connection
// usable for diagnostics; the reader calls then report ErrZoteroNotConfigured.
func (m *literatureModule) ensure(ctx context.Context) error {
	status, err := m.conn.Status(ctx)
	if err != nil {
		return err
	}
	dir := ""
	if status.DataDir != nil && status.DataDir.Valid {
		dir = status.DataDir.Path
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != nil && dir == m.dir {
		return nil
	}
	var sqlite *zotero.SQLiteReader
	if dir != "" {
		reader, err := zotero.NewSQLiteReader(dir)
		if err != nil && !errors.Is(err, literature.ErrZoteroDirInvalid) {
			return err
		}
		sqlite = reader
	}
	// The previous directory handle is released before the new one opens, so
	// changing the Zotero directory does not leak a database connection.
	if m.source != nil {
		_ = m.sqlite.Close()
	}
	source := zotero.NewSource(m.conn, sqlite, nil)
	options := library.Options{Clock: m.clock, Zotero: source}
	if m.search != nil {
		options.Indexer = m.search
	}
	m.sqlite = sqlite
	m.source = source
	m.lib = library.New(m.ws, options)
	m.dir = dir
	return nil
}

// Close releases the Zotero database handle. The workspace switch calls it
// before the workspace itself closes.
func (m *literatureModule) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if m.sqlite != nil {
		err = m.sqlite.Close()
	}
	m.sqlite = nil
	m.source = nil
	m.lib = nil
	m.dir = ""
	return err
}

// ZoteroStatus implements pages.LiteratureAPI. It reads the connection
// directly, so an unconfigured or invalid directory is reported instead of an
// error.
func (m *literatureModule) ZoteroStatus(ctx context.Context) (literature.ZoteroConnectionStatus, error) {
	return m.conn.Status(ctx)
}

func (m *literatureModule) MetadataSource(ctx context.Context) (literature.MetadataSource, string, error) {
	if err := m.ensure(ctx); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	source := m.source
	m.mu.Unlock()
	if source == nil {
		return "", "", literature.ErrZoteroNotConfigured
	}
	return source.Status(ctx)
}

// reader returns the live Zotero reader for browsing calls.
func (m *literatureModule) reader(ctx context.Context) (literature.ZoteroReader, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	source := m.source
	m.mu.Unlock()
	if source == nil {
		return nil, literature.ErrZoteroNotConfigured
	}
	return source.Reader(ctx)
}

func (m *literatureModule) Collections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error) {
	reader, err := m.reader(ctx)
	if err != nil {
		return nil, err
	}
	return reader.ListCollections(ctx, orDefault(libraryID, personalLibrary))
}

func (m *literatureModule) Items(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	reader, err := m.reader(ctx)
	if err != nil {
		return literature.ItemPage{}, err
	}
	if opts.Limit <= 0 {
		opts.Limit = literatureItemsPerPage
	}
	return reader.ListItems(ctx, orDefault(libraryID, personalLibrary), collectionKey, opts)
}

func (m *literatureModule) Attachments(ctx context.Context, libraryID, itemKey string) ([]literature.ZoteroAttachment, error) {
	reader, err := m.reader(ctx)
	if err != nil {
		return nil, err
	}
	return reader.ListAttachments(ctx, orDefault(libraryID, personalLibrary), itemKey)
}

// Preview is read-only: it lists what Import would do without touching files
// or databases (§5.4).
func (m *literatureModule) Preview(ctx context.Context, sel literature.ImportSelection) (literature.ImportPreview, error) {
	if err := m.ensure(ctx); err != nil {
		return literature.ImportPreview{}, err
	}
	m.mu.Lock()
	lib := m.lib
	m.mu.Unlock()
	if lib == nil {
		return literature.ImportPreview{}, literature.ErrZoteroNotConfigured
	}
	return lib.Preview(ctx, sel)
}

// Import runs the pipeline as the in-app user. Progress reaches the caller's
// task, which renders it in the shell's task strip.
func (m *literatureModule) Import(ctx context.Context, sel literature.ImportSelection, progress literature.ProgressFunc) (literature.ImportResult, error) {
	if err := m.ensure(ctx); err != nil {
		return literature.ImportResult{}, err
	}
	m.mu.Lock()
	lib := m.lib
	m.mu.Unlock()
	if lib == nil {
		return literature.ImportResult{}, literature.ErrZoteroNotConfigured
	}
	return lib.Import(ctx, sel, literature.ActorUser, progress)
}

func (m *literatureModule) ProjectPapers(ctx context.Context, projectID string) ([]literature.ProjectPaper, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	lib := m.lib
	m.mu.Unlock()
	if lib == nil {
		return nil, literature.ErrZoteroNotConfigured
	}
	page, err := lib.QueryProjectPapers(ctx, projectID, literature.PaperFilter{
		Sort:  literature.SortImportedAt,
		Desc:  true,
		Limit: projectPaperLimit,
	})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// literatureItemsPerPage is the browser page size; the Zotero reader accepts at
// most 100 items per call (SPEC-04 §5.2).
const literatureItemsPerPage = 100

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
