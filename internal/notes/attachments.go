// attachments.go implements note image attachments (SPEC-05 §5.6, R-05-13,
// acceptance 13). Images live under
// projects/<project-id>/attachments/<note-id>/<sha256[:12]>-<sanitized-name>
// and are referenced from the Markdown body through the note-relative path
// ../attachments/<note-id>/<file>, so an exported project folder keeps its
// images working. Same bytes are stored once (content-hash dedup). Nothing
// here ever deletes a file; CheckReferences only reports (§10 O-09).

package notes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/paths"
)

// ErrAttachmentValidation reports a rejected image or note id: unknown or
// deleted note, empty/oversized data, non-image content. A rejection leaves
// no file behind.
var ErrAttachmentValidation = errors.New("invalid attachment")

// DefaultMaxAttachmentBytes is the size limit used when AddImageInput.MaxBytes
// is 0 (SPEC-05 §5.6: 20 MiB default).
const DefaultMaxAttachmentBytes int64 = 20 << 20

// attHashNameLen is how many hex characters of the content sha256 go into the
// stored file name.
const attHashNameLen = 12

// attMaxBaseRunes caps the sanitized original base name.
const attMaxBaseRunes = 80

// attImageType is a detected image kind with its canonical extension.
type attImageType struct {
	ext string // canonical extension, includes the dot
}

var (
	attPNG  = attImageType{".png"}
	attJPEG = attImageType{".jpg"}
	attGIF  = attImageType{".gif"}
	attWEBP = attImageType{".webp"}
	attSVG  = attImageType{".svg"}
)

// attMarkdownLink matches inline Markdown images and links and captures the
// destination (with optional <...> wrapping, optional title ignored).
var attMarkdownLink = regexp.MustCompile(`!?\[[^\]]*\]\(\s*<?([^)>\s]+)>?[^)]*\)`)

// attachmentService is the AttachmentService implementation for one project.
type attachmentService struct {
	projectID string
	db        *sql.DB
	p         paths.Paths
	clk       clock.Clock
}

// NewAttachmentService returns the attachment service of one project. A nil
// clock uses clock.System.
func NewAttachmentService(projectID string, db *sql.DB, p paths.Paths, clk clock.Clock) AttachmentService {
	if clk == nil {
		clk = clock.System{}
	}
	return &attachmentService{projectID: projectID, db: db, p: p, clk: clk}
}

// AddImage stores one image for a live note. It streams at most cap+1 bytes
// from in.Data (cap = in.MaxBytes or DefaultMaxAttachmentBytes), rejects
// empty, oversize and non-image content without touching the disk, and writes
// accepted bytes atomically. Identical content reuses the existing file.
func (s *attachmentService) AddImage(ctx context.Context, in AddImageInput) (*Attachment, error) {
	if err := s.requireLiveNote(ctx, in.NoteID); err != nil {
		return nil, err
	}
	limit := in.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxAttachmentBytes
	}
	data, err := io.ReadAll(io.LimitReader(in.Data, limit+1))
	if err != nil {
		return nil, fmt.Errorf("attachments: read body: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty image data", ErrAttachmentValidation)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: image exceeds %d bytes", ErrAttachmentValidation, limit)
	}
	kind, ok := attDetectImage(data)
	if !ok {
		return nil, fmt.Errorf("%w: only png, jpeg, gif, webp and svg images are accepted",
			ErrAttachmentValidation)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	prefix := hash[:attHashNameLen]

	dirRel := s.p.AttachmentsDir(s.projectID, in.NoteID)
	dirAbs, err := s.p.Abs(dirRel)
	if err != nil {
		return nil, fmt.Errorf("attachments: resolve dir: %w", err)
	}
	// Content dedup: a file with the same hash prefix is reused when its full
	// hash matches, so identical bytes exist exactly once.
	names, err := os.ReadDir(dirAbs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("attachments: scan dir: %w", err)
	}
	for _, e := range names {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasPrefix(name, prefix+"-") {
			continue
		}
		cand := filepath.Join(dirAbs, name)
		sum2, err := attFileHash(cand)
		if err == nil && hex.EncodeToString(sum2) == hash {
			return s.attachment(dirRel, in.NoteID, name, hash, cand), nil
		}
	}

	name := prefix + "-" + attSanitizeBase(in.Filename) + kind.ext
	abs := filepath.Join(dirAbs, name)
	if _, err := notefile.WriteAtomic(abs, data); err != nil {
		return nil, fmt.Errorf("attachments: write: %w", err)
	}
	return s.attachment(dirRel, in.NoteID, name, hash, abs), nil
}

// ListAttachments returns every stored image of one note, hashed and sized,
// ordered by file name. A note without an attachment directory yields none.
func (s *attachmentService) ListAttachments(ctx context.Context, noteID string) ([]*Attachment, error) {
	if !ids.Valid(noteID) {
		return nil, fmt.Errorf("%w: note id %q is not a UUIDv7", errs.ErrBadEntityID, noteID)
	}
	dirRel := s.p.AttachmentsDir(s.projectID, noteID)
	dirAbs, err := s.p.Abs(dirRel)
	if err != nil {
		return nil, fmt.Errorf("attachments: resolve dir: %w", err)
	}
	entries, err := os.ReadDir(dirAbs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attachments: list dir: %w", err)
	}
	var out []*Attachment
	for _, e := range entries { // ReadDir is sorted by name
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue // skip dotfiles and temp files
		}
		abs := filepath.Join(dirAbs, name)
		sum, err := attFileHash(abs)
		if err != nil {
			return nil, fmt.Errorf("attachments: hash %s: %w", name, err)
		}
		out = append(out, s.attachment(dirRel, noteID, name, hex.EncodeToString(sum), abs))
	}
	return out, nil
}

// CheckReferences compares the image references in the note body with the
// files on disk and reports both directions: referenced-but-missing and
// on-disk-but-unreferenced. It never deletes anything and never fails just
// because the body file is missing (a missing body makes every stored file
// unreferenced).
func (s *attachmentService) CheckReferences(ctx context.Context, noteID string) ([]AttachmentIssue, error) {
	if !ids.Valid(noteID) {
		return nil, fmt.Errorf("%w: note id %q is not a UUIDv7", errs.ErrBadEntityID, noteID)
	}
	var (
		filePath  string
		fileState string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT file_path, file_state FROM notes WHERE id = ?`, noteID).Scan(&filePath, &fileState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: no such note %s", ErrAttachmentValidation, noteID)
	}
	if err != nil {
		return nil, fmt.Errorf("attachments: load note: %w", err)
	}

	body := ""
	if fileState != string(BodyMissing) {
		abs, perr := s.p.Abs(filePath)
		if perr == nil {
			raw, rerr := os.ReadFile(abs)
			switch {
			case rerr == nil:
				body = string(raw)
			case !errors.Is(rerr, os.ErrNotExist):
				return nil, fmt.Errorf("attachments: read body: %w", rerr)
			}
		}
	}

	dirRel := s.p.AttachmentsDir(s.projectID, noteID)
	dirAbs, err := s.p.Abs(dirRel)
	if err != nil {
		return nil, fmt.Errorf("attachments: resolve dir: %w", err)
	}
	marker := "attachments/" + noteID + "/"
	referenced := make(map[string]bool)
	var issues []AttachmentIssue
	for _, m := range attMarkdownLink.FindAllStringSubmatch(body, -1) {
		target := m[1]
		decoded, derr := url.PathUnescape(target)
		if derr != nil {
			decoded = target
		}
		idx := strings.Index(decoded, marker)
		if idx < 0 {
			continue
		}
		file := decoded[idx+len(marker):]
		if file == "" || strings.ContainsAny(file, `/\`) || file == ".." ||
			strings.HasPrefix(file, ".") {
			issues = append(issues, AttachmentIssue{
				Kind:         AttachmentMissingReference,
				NoteID:       noteID,
				MarkdownPath: target,
				Detail:       "reference escapes the attachment directory",
			})
			continue
		}
		if referenced[file] {
			continue
		}
		referenced[file] = true
		if _, serr := os.Stat(filepath.Join(dirAbs, file)); serr != nil {
			issues = append(issues, AttachmentIssue{
				Kind:         AttachmentMissingReference,
				NoteID:       noteID,
				MarkdownPath: target,
				Detail:       "referenced image is not on disk",
			})
		}
	}

	entries, err := os.ReadDir(dirAbs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("attachments: list dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || referenced[name] {
			continue
		}
		issues = append(issues, AttachmentIssue{
			Kind:         AttachmentUnreferencedFile,
			NoteID:       noteID,
			MarkdownPath: "../" + marker + name,
			Path:         dirRel + "/" + name,
			Detail:       "file on disk is not referenced by the body",
		})
	}
	return issues, nil
}

// attachment assembles the returned value for one stored file.
func (s *attachmentService) attachment(dirRel, noteID, name, hash, absPath string) *Attachment {
	att := &Attachment{
		RelativePath: dirRel + "/" + name,
		MarkdownPath: "../attachments/" + noteID + "/" + name,
		SHA256:       hash,
	}
	if info, err := os.Stat(absPath); err == nil {
		att.Bytes = info.Size()
	}
	return att
}

// requireLiveNote validates the note id and that the note exists and is not
// soft-deleted.
func (s *attachmentService) requireLiveNote(ctx context.Context, noteID string) error {
	if !ids.Valid(noteID) {
		return fmt.Errorf("%w: note id %q is not a UUIDv7", errs.ErrBadEntityID, noteID)
	}
	var deleted sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT deleted_at FROM notes WHERE id = ?`, noteID).
		Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no such note %s", ErrAttachmentValidation, noteID)
	}
	if err != nil {
		return fmt.Errorf("attachments: load note: %w", err)
	}
	if deleted.Valid {
		return fmt.Errorf("%w: note %s is deleted", errs.ErrEntityDeleted, noteID)
	}
	return nil
}

// attDetectImage classifies the leading bytes. png/jpeg/gif/webp are matched
// by magic bytes; svg is XML or an <svg root after an optional UTF-8 BOM and
// leading whitespace (http.DetectContentType reports those as text/xml).
func attDetectImage(data []byte) (attImageType, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return attPNG, true
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return attJPEG, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return attGIF, true
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return attWEBP, true
	}
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	head = bytes.TrimLeft(head, " \t\r\n")
	lower := bytes.ToLower(head)
	if !bytes.HasPrefix(lower, []byte("<?xml")) && !bytes.HasPrefix(lower, []byte("<svg")) &&
		!bytes.HasPrefix(lower, []byte("<!doctype")) {
		return attImageType{}, false
	}
	// An XML declaration or doctype alone is not an image: require the svg
	// root within the head.
	if bytes.HasPrefix(lower, []byte("<svg")) {
		return attSVG, true
	}
	if bytes.Contains(lower, []byte("<svg")) {
		return attSVG, true
	}
	return attImageType{}, false
}

// attSanitizeBase turns a user file name into a safe base: the last path
// segment without its extension, filtered to letters, digits, '.', '_' and
// '-', separators collapsed, leading dots/separators and trailing dots stripped
// and the result capped to attMaxBaseRunes runes. The stored extension comes
// from the detected content type, never from the input.
func attSanitizeBase(filename string) string {
	filename = filepath.Base(filepath.FromSlash(filename))
	if i := strings.LastIndexByte(filename, '.'); i > 0 {
		filename = filename[:i]
	}
	var b strings.Builder
	prevSep := false
	for _, r := range filename {
		switch {
		case r == '.' || r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSep = false
		case !prevSep:
			b.WriteRune('-')
			prevSep = true
		}
	}
	base := strings.TrimLeft(b.String(), ".-_") // strip leading dots/separators
	base = strings.TrimRight(base, ".-")        // trailing dot is unsafe on Windows
	if runes := []rune(base); len(runes) > attMaxBaseRunes {
		base = strings.TrimRight(string(runes[:attMaxBaseRunes]), ".-")
	}
	if base == "" {
		base = "image"
	}
	return base
}

// attFileHash returns the raw sha256 of a file's content.
func attFileHash(abs string) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
