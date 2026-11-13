package notes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/notes/migrations"
)

// attEnv is the shared fixture for the attachment tests.
type attEnv struct {
	t    *testing.T
	fix  *foundationtest.Fixture
	db   *sql.DB
	svc  AttachmentService
	root string // absolute workspace root
}

func attNewFixture(t *testing.T) *attEnv {
	t.Helper()
	fix, err := foundationtest.NewWithProject(t.TempDir(),
		foundationtest.Options{Migrations: migrations.MustAll()})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = fix.Close() })
	return &attEnv{
		t:    t,
		fix:  fix,
		db:   fix.Project.DB(),
		svc:  NewAttachmentService(fix.ProjectID, fix.Project.DB(), fix.Workspace.Paths(), fix.Clock),
		root: fix.Workspace.Paths().Root(),
	}
}

// attNewNote inserts a notes row and writes its Markdown body file, mirroring
// what the note service stores (path layout projects/<pid>/notes/<id>.md).
func (e *attEnv) attNewNote(body string) string {
	e.t.Helper()
	return e.attNewNoteState(body, "ok")
}

func (e *attEnv) attNewNoteState(body, state string) string {
	e.t.Helper()
	id := ids.MustNew(e.fix.Clock)
	rel := e.fix.Workspace.Paths().NoteFile(e.fix.ProjectID, id)
	abs, err := e.fix.Workspace.Paths().Abs(rel)
	if err != nil {
		e.t.Fatalf("abs: %v", err)
	}
	if state == "ok" {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			e.t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			e.t.Fatalf("write body: %v", err)
		}
	}
	now := e.attNow()
	if _, err := e.db.Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_state)
		 VALUES (?, '', ?, 'scratchpad', ?, ?, ?)`,
		id, rel, now, now, state); err != nil {
		e.t.Fatalf("seed note: %v", err)
	}
	return id
}

func (e *attEnv) attSetBody(noteID, body string) {
	e.t.Helper()
	var rel string
	if err := e.db.QueryRow(`SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&rel); err != nil {
		e.t.Fatalf("load body path: %v", err)
	}
	abs, err := e.fix.Workspace.Paths().Abs(rel)
	if err != nil {
		e.t.Fatalf("abs: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		e.t.Fatalf("write body: %v", err)
	}
}

func (e *attEnv) attNow() string {
	e.t.Helper()
	return e.fix.Clock.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// attDirEntries lists the note's attachment directory (non-dot files only);
// a missing directory yields nil.
func (e *attEnv) attDirEntries(noteID string) []os.DirEntry {
	e.t.Helper()
	abs, err := e.fix.Workspace.Paths().Abs(
		e.fix.Workspace.Paths().AttachmentsDir(e.fix.ProjectID, noteID))
	if err != nil {
		e.t.Fatalf("abs: %v", err)
	}
	entries, err := os.ReadDir(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatalf("readdir: %v", err)
	}
	var out []os.DirEntry
	for _, en := range entries {
		if !strings.HasPrefix(en.Name(), ".") {
			out = append(out, en)
		}
	}
	return out
}

// Tiny image fixtures. png/jpeg/gif are encoder output (valid files); webp is
// a structurally valid RIFF container; svg is plain XML.
func attBytesPNG() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func attBytesJPEG() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func attBytesGIF() []byte {
	var buf bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.White})
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func attBytesWEBP() []byte {
	payload := []byte{0x2f, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00} // chunk size 0, padded
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write([]byte{byte(4 + 8 + len(payload)), 0, 0, 0}) // little-endian size
	b.WriteString("WEBP")
	b.WriteString("VP8 ")
	b.Write([]byte{byte(len(payload)), 0, 0, 0})
	b.Write(payload)
	return b.Bytes()
}

func attBytesSVG() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`)
}

func TestAttachmentAddImageTypes(t *testing.T) {
	e := attNewFixture(t)
	ctx := context.Background()
	cases := []struct {
		data []byte
		ext  string
		name string
	}{
		{attBytesPNG(), ".png", "pic.png"},
		{attBytesJPEG(), ".jpg", "photo.jpeg"},
		{attBytesGIF(), ".gif", "anim.gif"},
		{attBytesWEBP(), ".webp", "icon.webp"},
		{attBytesSVG(), ".svg", "diagram.svg"},
	}
	for i, tc := range cases {
		noteID := e.attNewNote("")
		att, err := e.svc.AddImage(ctx, AddImageInput{
			NoteID: noteID, Filename: tc.name, Data: bytes.NewReader(tc.data),
		})
		if err != nil {
			t.Fatalf("case %d (%s): AddImage: %v", i, tc.name, err)
		}
		sum := sha256.Sum256(tc.data)
		wantHash := hex.EncodeToString(sum[:])
		if att.SHA256 != wantHash {
			t.Fatalf("case %d: hash %s want %s", i, att.SHA256, wantHash)
		}
		if att.Bytes != int64(len(tc.data)) {
			t.Fatalf("case %d: bytes %d want %d", i, att.Bytes, len(tc.data))
		}
		wantRel := "projects/" + e.fix.ProjectID + "/attachments/" + noteID + "/"
		if !strings.HasPrefix(att.RelativePath, wantRel) ||
			!strings.HasSuffix(att.RelativePath, tc.ext) {
			t.Fatalf("case %d: relative path %q not under %q or missing ext %q",
				i, att.RelativePath, wantRel, tc.ext)
		}
		wantMD := "../attachments/" + noteID + "/" + filepath.Base(att.RelativePath)
		if att.MarkdownPath != wantMD {
			t.Fatalf("case %d: markdown path %q want %q", i, att.MarkdownPath, wantMD)
		}
		if att.RelativePath[:len(wantRel)] != wantRel ||
			strings.Contains(strings.TrimPrefix(att.RelativePath, wantRel), "/") {
			t.Fatalf("case %d: file not directly inside the note dir: %q", i, att.RelativePath)
		}
		abs := filepath.Join(e.root, filepath.FromSlash(att.RelativePath))
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("case %d: stored file missing: %v", i, err)
		}
		prefix := wantHash[:12]
		if base := filepath.Base(att.RelativePath); !strings.HasPrefix(base, prefix+"-") ||
			!strings.HasSuffix(base, "-"+strings.TrimSuffix(tc.name, filepath.Ext(tc.name))+tc.ext) {
			t.Fatalf("case %d: name %q not <hash12>-<base><ext>", i, base)
		}
	}
}

func TestAttachmentRejections(t *testing.T) {
	e := attNewFixture(t)
	ctx := context.Background()
	data := attBytesPNG()

	// Non-image content, empty data and oversize data are rejected and leave
	// nothing on disk (SPEC-05 §5.6: no half files).
	bad := []struct {
		name    string
		in      AddImageInput
		fixture []byte
	}{
		{"text", AddImageInput{Filename: "notes.txt"}, []byte("This is definitely not an image.\n")},
		{"exe", AddImageInput{Filename: "installer.exe"}, append([]byte("MZ\x90\x00\x03\x00\x00\x04"), data...)},
		{"xml-no-svg", AddImageInput{Filename: "data.xml"}, []byte(`<?xml version="1.0"?><root><a/></root>`)},
	}
	for _, tc := range bad {
		noteID := e.attNewNote("")
		in := tc.in
		in.NoteID = noteID
		in.Data = bytes.NewReader(tc.fixture)
		if _, err := e.svc.AddImage(ctx, in); !errors.Is(err, ErrAttachmentValidation) {
			t.Fatalf("%s: err = %v, want ErrAttachmentValidation", tc.name, err)
		}
		if entries := e.attDirEntries(noteID); len(entries) != 0 {
			t.Fatalf("%s: rejected upload left %d files", tc.name, len(entries))
		}
	}

	// Zero-byte upload.
	noteID := e.attNewNote("")
	if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "x.png",
		Data: bytes.NewReader(nil)}); !errors.Is(err, ErrAttachmentValidation) {
		t.Fatalf("empty: err = %v", err)
	}

	// Oversize: the cap is enforced while streaming (one byte over is enough).
	noteID = e.attNewNote("")
	if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "big.png",
		Data: bytes.NewReader(data), MaxBytes: int64(len(data) - 1)}); !errors.Is(err, ErrAttachmentValidation) {
		t.Fatalf("oversize: err = %v", err)
	}
	if entries := e.attDirEntries(noteID); len(entries) != 0 {
		t.Fatalf("oversize left %d files", len(entries))
	}
	// Exactly at the cap succeeds.
	noteID = e.attNewNote("")
	if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "big.png",
		Data: bytes.NewReader(data), MaxBytes: int64(len(data))}); err != nil {
		t.Fatalf("at-cap: %v", err)
	}

	// Unknown and deleted notes are rejected.
	unknown := ids.MustNew(e.fix.Clock)
	if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: unknown, Filename: "a.png",
		Data: bytes.NewReader(data)}); !errors.Is(err, ErrAttachmentValidation) {
		t.Fatalf("unknown note: err = %v", err)
	}
	noteID = e.attNewNote("")
	if _, err := e.db.Exec(`UPDATE notes SET deleted_at = ? WHERE id = ?`, e.attNow(), noteID); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "a.png",
		Data: bytes.NewReader(data)}); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("deleted note: err = %v", err)
	}
	if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: "not-a-uuid", Filename: "a.png",
		Data: bytes.NewReader(data)}); !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatalf("bad note id: err = %v", err)
	}
}

func TestAttachmentSanitizesFilenames(t *testing.T) {
	e := attNewFixture(t)
	ctx := context.Background()

	// Path components are stripped; everything stays inside the note's
	// attachment directory and no traversal survives.
	for _, tricky := range []string{"../../evil.png", `..\..\evil.png`, "..", ".", "a b*c?d.png", strings.Repeat("n", 300) + ".png"} {
		noteID := e.attNewNote("")
		att, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: tricky,
			Data: bytes.NewReader(attBytesPNG())})
		if err != nil {
			t.Fatalf("filename %q: %v", tricky, err)
		}
		dirRel := e.fix.Workspace.Paths().AttachmentsDir(e.fix.ProjectID, noteID)
		if filepath.Dir(att.RelativePath) != filepath.FromSlash(dirRel) &&
			!strings.HasPrefix(att.RelativePath, dirRel+"/") {
			t.Fatalf("filename %q escaped: %q", tricky, att.RelativePath)
		}
		if err := e.fix.Workspace.Paths().ValidateInternal(att.RelativePath); err != nil {
			t.Fatalf("filename %q produced non-internal path: %v", tricky, err)
		}
		base := filepath.Base(att.RelativePath)
		if strings.Contains(base, "..") || strings.ContainsAny(base, `/\`) {
			t.Fatalf("filename %q survived sanitising: %q", tricky, base)
		}
		if base := filepath.Base(att.RelativePath); !strings.HasPrefix(base, att.SHA256[:attHashNameLen]+"-") ||
			len(base) > attHashNameLen+1+attMaxBaseRunes+len(".png") {
			t.Fatalf("filename %q produced name %q (prefix or cap wrong)", tricky, base)
		}
		if entries := e.attDirEntries(noteID); len(entries) != 1 {
			t.Fatalf("filename %q: %d files stored", tricky, len(entries))
		}
	}

	// The traversal filename keeps the readable base.
	noteID := e.attNewNote("")
	att, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "../../evil.png",
		Data: bytes.NewReader(attBytesPNG())})
	if err != nil {
		t.Fatalf("evil: %v", err)
	}
	if base := filepath.Base(att.RelativePath); !strings.HasSuffix(base, "-evil.png") {
		t.Fatalf("expected sanitized -evil.png suffix, got %q", base)
	}
}

func TestAttachmentIdempotentSameContent(t *testing.T) {
	e := attNewFixture(t)
	ctx := context.Background()
	noteID := e.attNewNote("")
	data := attBytesPNG()

	first, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "shot.png",
		Data: bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "shot.png",
		Data: bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.RelativePath != second.RelativePath || first.MarkdownPath != second.MarkdownPath {
		t.Fatalf("paths differ: %q vs %q", first.RelativePath, second.RelativePath)
	}
	if entries := e.attDirEntries(noteID); len(entries) != 1 {
		t.Fatalf("%d files for identical bytes, want 1", len(entries))
	}
	abs := filepath.Join(e.root, filepath.FromSlash(first.RelativePath))
	raw, err := os.ReadFile(abs)
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("stored bytes differ: %v", err)
	}
}

func TestAttachmentListHashes(t *testing.T) {
	e := attNewFixture(t)
	ctx := context.Background()
	noteID := e.attNewNote("")

	pngData := attBytesPNG()
	gifData := attBytesGIF()
	for _, d := range [][]byte{gifData, pngData} {
		if _, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "x.png",
			Data: bytes.NewReader(d)}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	// A dotfile/temp artifact must not be listed.
	dirAbs := filepath.Join(e.root, filepath.FromSlash(
		e.fix.Workspace.Paths().AttachmentsDir(e.fix.ProjectID, noteID)))
	if err := os.WriteFile(filepath.Join(dirAbs, ".x.png.tmp-abc"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("temp file: %v", err)
	}

	list, err := e.svc.ListAttachments(ctx, noteID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d attachments, want 2 (temp skipped)", len(list))
	}
	hashOf := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	want := map[string]int64{hashOf(gifData): int64(len(gifData)), hashOf(pngData): int64(len(pngData))}
	var prev string
	for _, a := range list {
		if a.RelativePath <= prev {
			t.Fatalf("not ordered by file name: %q after %q", a.RelativePath, prev)
		}
		prev = a.RelativePath
		if _, ok := want[a.SHA256]; !ok {
			t.Fatalf("unexpected hash %s", a.SHA256)
		}
		if a.Bytes != want[a.SHA256] {
			t.Fatalf("bytes %d want %d for %s", a.Bytes, want[a.SHA256], a.SHA256)
		}
		if !strings.HasPrefix(a.MarkdownPath, "../attachments/"+noteID+"/") {
			t.Fatalf("markdown path %q", a.MarkdownPath)
		}
	}

	empty := e.attNewNote("")
	if list, err := e.svc.ListAttachments(ctx, empty); err != nil || len(list) != 0 {
		t.Fatalf("note without dir: %v, %v", list, err)
	}
	if _, err := e.svc.ListAttachments(ctx, "bad-id"); !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestAttachmentCheckReferences(t *testing.T) {
	e := attNewFixture(t)
	ctx := context.Background()
	noteID := e.attNewNote("")

	present, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "keep.png",
		Data: bytes.NewReader(attBytesPNG())})
	if err != nil {
		t.Fatalf("add png: %v", err)
	}
	ghost, err := e.svc.AddImage(ctx, AddImageInput{NoteID: noteID, Filename: "loose.gif",
		Data: bytes.NewReader(attBytesGIF())})
	if err != nil {
		t.Fatalf("add gif: %v", err)
	}
	missing := "../attachments/" + noteID + "/000000000000-gone.png"

	// Body references the png (twice), a missing file and a traversal target;
	// the gif on disk stays unreferenced.
	e.attSetBody(noteID, strings.Join([]string{
		`![first](` + present.MarkdownPath + `)`,
		`![again](` + present.MarkdownPath + `)`,
		`![gone](` + missing + `)`,
		`![trick](../attachments/` + noteID + `/../../secret.txt)`,
	}, "\n"))

	issues, err := e.svc.CheckReferences(ctx, noteID)
	if err != nil {
		t.Fatalf("CheckReferences: %v", err)
	}
	var miss, unref int
	for _, is := range issues {
		switch is.Kind {
		case AttachmentMissingReference:
			miss++
			if is.NoteID != noteID {
				t.Fatalf("issue note id %q", is.NoteID)
			}
		case AttachmentUnreferencedFile:
			unref++
			if is.Path != ghost.RelativePath {
				t.Fatalf("unreferenced path %q want %q", is.Path, ghost.RelativePath)
			}
		}
	}
	if miss != 2 || unref != 1 {
		t.Fatalf("issues = %+v, want 2 missing (gone + traversal) and 1 unreferenced", issues)
	}

	// Fixing the body silences everything except the real missing file; the
	// check must never delete anything (O-09).
	e.attSetBody(noteID, `![both](`+present.MarkdownPath+`) ![also](`+ghost.MarkdownPath+`) ![gone](`+missing+`)`)
	issues, err = e.svc.CheckReferences(ctx, noteID)
	if err != nil {
		t.Fatalf("CheckReferences after fix: %v", err)
	}
	if len(issues) != 1 || issues[0].Kind != AttachmentMissingReference ||
		!strings.Contains(issues[0].MarkdownPath, "gone.png") {
		t.Fatalf("after fix issues = %+v", issues)
	}
	if entries := e.attDirEntries(noteID); len(entries) != 2 {
		t.Fatalf("CheckReferences deleted files: %d left", len(entries))
	}

	// A project-relative reference resolves the same way.
	e.attSetBody(noteID, `![both](projects/`+e.fix.ProjectID+`/attachments/`+noteID+`/`+
		filepath.Base(present.RelativePath)+`) ![also](`+ghost.MarkdownPath+`)`)
	issues, err = e.svc.CheckReferences(ctx, noteID)
	if err != nil {
		t.Fatalf("CheckReferences project-relative: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("project-relative refs unresolved: %+v", issues)
	}

	// Missing body (file_state='missing'): reported, not an error; every file
	// on disk is unreferenced.
	missingBodyNote := e.attNewNoteState("", "missing")
	a2, err := e.svc.AddImage(ctx, AddImageInput{NoteID: missingBodyNote, Filename: "a.png",
		Data: bytes.NewReader(attBytesPNG())})
	if err != nil {
		t.Fatalf("add to missing-body note: %v", err)
	}
	issues, err = e.svc.CheckReferences(ctx, missingBodyNote)
	if err != nil {
		t.Fatalf("CheckReferences missing body: %v", err)
	}
	if len(issues) != 1 || issues[0].Kind != AttachmentUnreferencedFile ||
		issues[0].Path != a2.RelativePath {
		t.Fatalf("missing-body issues = %+v", issues)
	}

	// Unknown note and malformed id.
	if _, err := e.svc.CheckReferences(ctx, ids.MustNew(e.fix.Clock)); !errors.Is(err, ErrAttachmentValidation) {
		t.Fatal("unknown note must fail")
	}
	if _, err := e.svc.CheckReferences(ctx, "nope"); !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatal("bad id must fail")
	}
}
