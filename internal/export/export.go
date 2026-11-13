// Package export implements the SPEC-03 §5.9 export service and the SPEC-03
// §4.4/§4.5 bundle layout.
//
// The service is deliberately two-phase: Plan performs every preflight check
// without touching the workspace (a caller can show a wizard page), Start runs
// the streamed snapshot. A bundle is therefore planned once and executed once;
// the job id equals the plan id (one plan has at most one job).
//
// Invariants worth knowing before editing:
//
//   - Never overwrite. The plan resolves a free output name; finalize re-checks
//     the target and publishes with a rename.
//   - Everything is streamed with a reused 1 MiB buffer. No file is read into
//     memory, and hash values come from the same pass that writes the bytes.
//   - Temporary artefacts live under <DestDir>/.researchos-export-<jobID>/ and
//     <DestDir>/.<base>.<ext>.part, both on the destination volume, so the final
//     rename never crosses a filesystem.
//   - A failure or cancellation removes only those temporary artefacts; an
//     existing output file is left untouched.
package export

import (
	"errors"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
	wssvc "researchos/internal/workspace"
)

// Format is the bundle container (SPEC-03 §5.9).
type Format string

const (
	FormatFolder Format = "folder"
	FormatZip    Format = "zip"
	FormatTarGz  Format = "tar.gz"
)

// Request describes one export. DestDir is an absolute directory; empty means
// <workspace>/exports. Actor is the additive field that keeps the §7.1
// project.exported event attributable; empty means the in-app user and only
// user/mcp are accepted.
type Request struct {
	ProjectID string
	DestDir   string
	Format    Format
	BaseName  string
	Actor     ports.Actor
}

// Plan is the preflight result. OutputPath does not exist yet.
type Plan struct {
	ID              string
	Request         Request
	OutputPath      string
	StagingPath     string
	Files           []PlannedFile
	TotalBytes      int64
	NoteCount       int
	PaperCount      int
	AttachmentCount int
	Missing         []MissingRef
	Warnings        []Warning
	ExpiresAt       time.Time
}

// Result describes a finished export.
type Result struct {
	JobID         string
	Path          string
	Format        Format
	Bytes         int64
	ArchiveSHA256 string
	FileCount     int
	PaperCount    int
	Duration      time.Duration
	Missing       []MissingRef
	Warnings      []Warning
}

// Progress is one progress sample. Current is a bundle-relative path.
type Progress struct {
	Phase      Phase
	FilesDone  int
	FilesTotal int
	BytesDone  int64
	BytesTotal int64
	Current    string
}

// Phase is the coarse progress phase (§5.9).
type Phase string

const (
	PhasePlanning        Phase = "planning"
	PhaseSnapshotDB      Phase = "snapshot_db"
	PhaseCopyNotes       Phase = "copy_notes"
	PhaseCopyAttachments Phase = "copy_attachments"
	PhaseCopyPapers      Phase = "copy_papers"
	PhaseFinalize        Phase = "finalize"
)

// FileKind classifies one planned bundle entry.
type FileKind string

const (
	KindProjectDB  FileKind = "project_db"
	KindNoteMD     FileKind = "note_md"
	KindAttachment FileKind = "attachment"
	KindPaperPDF   FileKind = "paper_pdf"
	KindMetaJSON   FileKind = "meta_json"
	KindManifest   FileKind = "manifest"
)

// PlannedFile is one file of the bundle. Meta JSON entries are generated, so
// their SourceAbs is empty and Bytes is 0.
type PlannedFile struct {
	// SourceAbs is the absolute workspace path of the source file.
	SourceAbs string
	// BundlePath is bundle-relative and always uses '/'.
	BundlePath string
	Kind       FileKind
	Bytes      int64
}

// MissingRef records a source the database references but the bundle cannot
// contain (§5.9 step 6).
type MissingRef struct {
	Kind         string // paper_pdf / note_md
	Ref          string // paper-id / note-id
	RecordedPath string // path as recorded in the database
}

// Warning is a non-fatal anomaly recorded in the manifest.
type Warning struct {
	Code string // symlink_skipped / changed_during_copy / hash_mismatch / path_rejected / attachment_dir_missing
	Path string
}

// Sentinel errors (§5.9). Callers match them with errors.Is.
var (
	ErrBusy               = errors.New("export: another job is running")
	ErrPlanExpired        = errors.New("export: plan expired")
	ErrInsufficientSpace  = errors.New("export: insufficient disk space")
	ErrNameConflict       = errors.New("export: output name conflict")
	ErrDestInsideProject  = errors.New("export: destination inside project directory")
	ErrFeatureUnavailable = errors.New("export: literature index unavailable")
)

// Options configures a Service. Every dependency is optional.
type Options struct {
	// Workspace is the open SPEC-01 workspace.
	Workspace *fworkspace.Workspace
	// Clock supplies plan expiry, timestamps and ids. Nil uses the workspace
	// clock, then clock.System.
	Clock clock.Clock
	// Locker serializes the short snapshot phase against project writes. Nil
	// uses an in-process locker.
	Locker ports.Locker
	// PaperIndex is the SPEC-04 literature port. Nil reads project_papers
	// directly from project.db.
	PaperIndex ports.PaperIndex

	// diskFree overrides the free-space probe. Testing seam.
	diskFree func(path string) (free, total int64, ok bool)
	// hook runs before each entry of a copy phase. Testing seam used to cancel
	// deterministically.
	hook func(stage, path string)
}

// Service is safe for concurrent use. It keeps pending plans and the single
// active job in memory: plans never survive a restart, and an interrupted
// process leaves at most a stale staging directory.
type Service struct {
	ws       *fworkspace.Workspace
	clock    clock.Clock
	locker   ports.Locker
	papers   ports.PaperIndex
	diskFree func(path string) (free, total int64, ok bool)
	hook     func(stage, path string)

	mu     sync.Mutex
	plans  map[string]*planState
	jobs   map[string]*job
	active *job
}

// New builds a Service from opts.
func New(opts Options) *Service {
	c := opts.Clock
	if c == nil && opts.Workspace != nil {
		c = opts.Workspace.Clock()
	}
	if c == nil {
		c = clock.System{}
	}
	s := &Service{
		ws:       opts.Workspace,
		clock:    c,
		locker:   opts.Locker,
		papers:   opts.PaperIndex,
		diskFree: opts.diskFree,
		hook:     opts.hook,
		plans:    make(map[string]*planState),
		jobs:     make(map[string]*job),
	}
	if s.locker == nil {
		s.locker = newMemoryLocker()
	}
	if s.diskFree == nil {
		s.diskFree = wssvc.DiskFree
	}
	return s
}

// extension returns the output suffix of f.
func (f Format) extension() string {
	switch f {
	case FormatZip:
		return ".zip"
	case FormatTarGz:
		return ".tar.gz"
	default:
		return ""
	}
}

// valid reports whether f is one of the three supported containers.
func (f Format) valid() bool {
	switch f {
	case FormatFolder, FormatZip, FormatTarGz:
		return true
	}
	return false
}

// label is the human wording used in the activity summary.
func (f Format) label() string {
	switch f {
	case FormatFolder:
		return "文件夹"
	case FormatZip:
		return "zip"
	case FormatTarGz:
		return "tar.gz"
	}
	return string(f)
}

// normalizeActor applies the §7.1 rule: empty means user, and only user/mcp
// may appear on a project event.
func normalizeActor(a ports.Actor) (ports.Actor, error) {
	switch a {
	case "":
		return ports.ActorUser, nil
	case ports.ActorUser, ports.ActorMCP:
		return a, nil
	default:
		return "", errors.New("export: actor " + string(a) + " must be user or mcp")
	}
}

// checkReady validates that the service is bound to a live workspace.
func (s *Service) checkReady() error {
	if s == nil || s.ws == nil {
		return errors.New("export: service has no workspace")
	}
	if s.ws.Global() == nil {
		return ErrFeatureUnavailable
	}
	return nil
}
