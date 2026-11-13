package library

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
)

// fakeReader is a minimal literature.ZoteroReader for attachment resolution.
type fakeReader struct {
	attachments []literature.ZoteroAttachment
	resolved    map[string]literature.ResolvedFile
	listErr     error
}

func (r *fakeReader) Source() literature.MetadataSource { return literature.SourceSQLite }
func (r *fakeReader) ListLibraries(context.Context) ([]literature.ZoteroLibrary, error) {
	return nil, nil
}
func (r *fakeReader) ListCollections(context.Context, string) ([]literature.ZoteroCollection, error) {
	return nil, nil
}
func (r *fakeReader) ListItems(context.Context, string, string, literature.ListItemsOptions) (literature.ItemPage, error) {
	return literature.ItemPage{}, nil
}
func (r *fakeReader) GetItem(context.Context, string, string) (literature.ZoteroItem, error) {
	return literature.ZoteroItem{}, errors.New("not implemented")
}
func (r *fakeReader) ListAttachments(context.Context, string, string) ([]literature.ZoteroAttachment, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.attachments, nil
}
func (r *fakeReader) ResolveAttachment(_ context.Context, att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	if resolved, ok := r.resolved[att.Key]; ok {
		return resolved, nil
	}
	return literature.ResolvedFile{}, errors.New("unresolved")
}

type fakeSource struct {
	reader literature.ZoteroReader
	err    error
}

func (s fakeSource) Reader(context.Context) (literature.ZoteroReader, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.reader, nil
}
func (s fakeSource) Status(context.Context) (literature.MetadataSource, string, error) {
	return literature.SourceSQLite, "", nil
}

func newPDFService(f *foundationtest.Fixture, source literature.ZoteroSource, opener Opener) *Service {
	return New(f.Workspace, Options{Clock: f.Clock, Zotero: source, Opener: opener})
}

func TestLocateImportedCopy(t *testing.T) {
	f := newFixture(t)
	service := newPDFService(f, nil, nil)
	ctx := context.Background()
	paperID := seedPaper(t, f, paperSeed{Key: "PDF1", Title: "pdf", Copy: true, Content: "hello pdf"})

	location, err := service.Locate(ctx, paperID, literature.PDFImportedCopy)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	wantPath, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperPDF(paperID))
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	if location.PaperID != paperID || location.Path != wantPath {
		t.Errorf("location = %+v, want path %s", location, wantPath)
	}
	if !location.Exists || location.Size != int64(len("hello pdf")) {
		t.Errorf("exists/size = %v/%d", location.Exists, location.Size)
	}
	if location.Hash != "hash-PDF1" {
		t.Errorf("hash = %q, want stored hash", location.Hash)
	}

	missing := seedPaper(t, f, paperSeed{Key: "PDF2", Title: "missing"})
	if _, err := service.Locate(ctx, missing, literature.PDFImportedCopy); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("missing copy error = %v, want ErrPaperFileMissing", err)
	}
	if _, err := service.Locate(ctx, ids.MustNew(f.Clock), literature.PDFImportedCopy); !errors.Is(err, literature.ErrPaperNotFound) {
		t.Errorf("unknown paper error = %v, want ErrPaperNotFound", err)
	}
	if _, err := service.Locate(ctx, paperID, literature.PDFTarget("bogus")); err == nil {
		t.Error("unknown target was accepted")
	}
}

func TestLocateZoteroSource(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := &fakeReader{
		attachments: []literature.ZoteroAttachment{
			{Key: "ATT1", ContentType: "application/pdf", Filename: "paper.pdf"},
		},
		resolved: map[string]literature.ResolvedFile{
			"ATT1": {Path: "/zotero/storage/ATT1/paper.pdf", Size: 42},
		},
	}
	service := newPDFService(f, fakeSource{reader: reader}, nil)

	paperID := seedPaper(t, f, paperSeed{Key: "ZOT1", Title: "zotero"})
	location, err := service.Locate(ctx, paperID, literature.PDFZoteroSource)
	if err != nil {
		t.Fatalf("locate original: %v", err)
	}
	if location.Path != "/zotero/storage/ATT1/paper.pdf" || location.Size != 42 {
		t.Errorf("location = %+v", location)
	}
	if location.Hash != "" {
		t.Errorf("hash = %q, sources must not be hashed", location.Hash)
	}
	if !location.Exists {
		t.Error("exists = false")
	}
}

func TestLocateZoteroSourceErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	noZotero := seedPaper(t, f, paperSeed{Key: "ZERR1", Title: "no zotero"})
	if _, err := newPDFService(f, nil, nil).Locate(ctx, noZotero, literature.PDFZoteroSource); !errors.Is(err, literature.ErrZoteroNotConfigured) {
		t.Errorf("error = %v, want ErrZoteroNotConfigured", err)
	}

	missingSource := seedPaper(t, f, paperSeed{Key: "ZERR2", Title: "source missing", SourceState: schema.PaperSourceMissing})
	reader := &fakeReader{}
	if _, err := newPDFService(f, fakeSource{reader: reader}, nil).Locate(ctx, missingSource, literature.PDFZoteroSource); !errors.Is(err, literature.ErrAttachmentUnresolved) {
		t.Errorf("missing source error = %v, want ErrAttachmentUnresolved", err)
	}

	noPDF := seedPaper(t, f, paperSeed{Key: "ZERR3", Title: "no pdf"})
	if _, err := newPDFService(f, fakeSource{reader: &fakeReader{}}, nil).Locate(ctx, noPDF, literature.PDFZoteroSource); !errors.Is(err, literature.ErrNoPDFAttachment) {
		t.Errorf("no pdf error = %v, want ErrNoPDFAttachment", err)
	}

	twoPDFs := seedPaper(t, f, paperSeed{Key: "ZERR4", Title: "two pdfs"})
	twoReader := &fakeReader{attachments: []literature.ZoteroAttachment{
		{Key: "A1", ContentType: "application/pdf"},
		{Key: "A2", Filename: "other.PDF"},
	}}
	if _, err := newPDFService(f, fakeSource{reader: twoReader}, nil).Locate(ctx, twoPDFs, literature.PDFZoteroSource); !errors.Is(err, literature.ErrAttachmentChoice) {
		t.Errorf("two pdfs error = %v, want ErrAttachmentChoice", err)
	}
}

// setAttachmentKey marks a paper as already resolved to a specific attachment.
func setAttachmentKey(ctx context.Context, f *foundationtest.Fixture, paperID, key string) error {
	return f.Global().InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE papers SET attachment_key = ? WHERE id = ?`, key, paperID)
		return err
	})
}

func TestLocateZoteroSourcePrefersAttachmentKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := &fakeReader{
		attachments: []literature.ZoteroAttachment{
			{Key: "FIRST", ContentType: "application/pdf"},
			{Key: "SECOND", ContentType: "application/pdf"},
		},
		resolved: map[string]literature.ResolvedFile{
			"SECOND": {Path: "/zotero/storage/SECOND/source.pdf", Size: 7},
		},
	}
	paperID := seedPaper(t, f, paperSeed{Key: "ZKEY", Title: "attachment key"})
	if err := setAttachmentKey(ctx, f, paperID, "SECOND"); err != nil {
		t.Fatalf("set attachment key: %v", err)
	}
	location, err := newPDFService(f, fakeSource{reader: reader}, nil).Locate(ctx, paperID, literature.PDFZoteroSource)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if location.Path != "/zotero/storage/SECOND/source.pdf" || location.Size != 7 {
		t.Errorf("location = %+v, want the recorded attachment", location)
	}
}

func TestLocateZoteroSourceUnknownAttachmentKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	paperID := seedPaper(t, f, paperSeed{Key: "ZKEY2", Title: "unknown attachment key"})
	if err := setAttachmentKey(ctx, f, paperID, "GONE"); err != nil {
		t.Fatalf("set attachment key: %v", err)
	}
	reader := &fakeReader{attachments: []literature.ZoteroAttachment{
		{Key: "OTHER", ContentType: "application/pdf"},
	}}
	if _, err := newPDFService(f, fakeSource{reader: reader}, nil).Locate(ctx, paperID, literature.PDFZoteroSource); !errors.Is(err, literature.ErrAttachmentUnresolved) {
		t.Errorf("error = %v, want ErrAttachmentUnresolved", err)
	}
}

func TestOpenImportedCopyUsesOpenerWithoutActivity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var targets []string
	opener := func(_ context.Context, target string) error {
		targets = append(targets, target)
		return nil
	}
	service := newPDFService(f, nil, opener)
	paperID := seedPaper(t, f, paperSeed{Key: "OPEN1", Title: "open", Copy: true})

	if err := service.Open(ctx, paperID, literature.PDFImportedCopy); err != nil {
		t.Fatalf("open: %v", err)
	}
	wantPath, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperPDF(paperID))
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	if len(targets) != 1 || targets[0] != wantPath {
		t.Fatalf("opener targets = %v, want [%s]", targets, wantPath)
	}
	if n := countAllActivities(t, f); n != 0 {
		t.Errorf("activities = %d, opening must not write activity", n)
	}

	if err := service.Open(ctx, paperID, ""); err != nil {
		t.Fatalf("open default target: %v", err)
	}
	if len(targets) != 2 || targets[1] != wantPath {
		t.Errorf("default target opened %v", targets)
	}
}

func TestOpenZoteroReaderMode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var targets []string
	opener := func(_ context.Context, target string) error {
		targets = append(targets, target)
		return nil
	}
	service := newPDFService(f, nil, opener)
	paperID := seedPaper(t, f, paperSeed{Key: "OPEN2", Title: "zotero mode", Copy: true})
	if err := f.Settings().Set(ctx, nil, "literature.pdf_open_mode", "zotero_reader"); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	if err := service.Open(ctx, paperID, literature.PDFImportedCopy); err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(targets) != 1 || targets[0] != "zotero://select/library/items/OPEN2" {
		t.Fatalf("opener targets = %v, want the Zotero URI", targets)
	}
	if n := countAllActivities(t, f); n != 0 {
		t.Errorf("activities = %d, opening must not write activity", n)
	}
}

func TestOpenZoteroReaderFallsBackToCopy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var targets []string
	opener := func(_ context.Context, target string) error {
		targets = append(targets, target)
		if strings.HasPrefix(target, "zotero://") {
			return errors.New("zotero not running")
		}
		return nil
	}
	service := newPDFService(f, nil, opener)
	paperID := seedPaper(t, f, paperSeed{Key: "OPEN3", Title: "fallback", Copy: true})
	if err := f.Settings().Set(ctx, nil, "literature.pdf_open_mode", "zotero_reader"); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	if err := service.Open(ctx, paperID, literature.PDFImportedCopy); err != nil {
		t.Fatalf("open with fallback: %v", err)
	}
	wantPath, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperPDF(paperID))
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("opener targets = %v, want URI then copy", targets)
	}
	if targets[0] != "zotero://select/library/items/OPEN3" || targets[1] != wantPath {
		t.Errorf("fallback targets = %v, want [URI %s]", targets, wantPath)
	}
}

func TestOpenMissingCopy(t *testing.T) {
	f := newFixture(t)
	opened := 0
	service := newPDFService(f, nil, func(context.Context, string) error {
		opened++
		return nil
	})
	paperID := seedPaper(t, f, paperSeed{Key: "OPEN4", Title: "missing"})
	err := service.Open(context.Background(), paperID, literature.PDFImportedCopy)
	if !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Fatalf("error = %v, want ErrPaperFileMissing", err)
	}
	if opened != 0 {
		t.Errorf("opener called %d times for a missing copy", opened)
	}
}

func TestOpenZoteroModeMissingCopyReportsBothFailures(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	service := newPDFService(f, nil, func(_ context.Context, target string) error {
		if strings.HasPrefix(target, "zotero://") {
			return errors.New("zotero not running")
		}
		return errors.New("no copy")
	})
	paperID := seedPaper(t, f, paperSeed{Key: "OPEN5", Title: "both missing"})
	if err := f.Settings().Set(ctx, nil, "literature.pdf_open_mode", "zotero_reader"); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	err := service.Open(ctx, paperID, literature.PDFImportedCopy)
	if !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("error = %v, want ErrPaperFileMissing", err)
	}
}

func countAllActivities(t *testing.T, f *foundationtest.Fixture) int {
	t.Helper()
	var n int
	if err := f.Project.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM activities`).Scan(&n); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	return n
}
