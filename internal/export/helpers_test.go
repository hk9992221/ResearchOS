package export

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	fworkspace "researchos/internal/foundation/workspace"
)

// newFixture builds a workspace with a default project and an export service.
func newFixture(t *testing.T, tweaks ...func(*Options)) (*foundationtest.Fixture, *Service) {
	t.Helper()
	f, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	opts := Options{Workspace: f.Workspace, Clock: f.Clock}
	for _, tweak := range tweaks {
		tweak(&opts)
	}
	return f, New(opts)
}

// addNote inserts a note row and, unless withFile is false, its markdown file.
func addNote(t *testing.T, f *foundationtest.Fixture, handle *fworkspace.ProjectHandle, deleted, withFile bool) string {
	t.Helper()
	id := ids.MustNew(f.Clock)
	rel := f.Workspace.Paths().NoteFile(handle.ID, id)
	if withFile {
		abs := filepath.Join(f.Root, filepath.FromSlash(rel))
		writeTestFile(t, abs, []byte("# note "+id+"\n"))
	}
	var deletedAt any
	if deleted {
		deletedAt = clock.Format(f.Clock.Now())
	}
	at := clock.Format(f.Clock.Now())
	if _, err := handle.DB().Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash, file_state, deleted_at)
		 VALUES (?, 'note', ?, 'scratchpad', ?, ?, '', 'ok', ?)`,
		id, rel, at, at, deletedAt); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	return id
}

// addNoteWithPath inserts a note with an arbitrary (possibly malicious) path.
func addNoteWithPath(t *testing.T, f *foundationtest.Fixture, handle *fworkspace.ProjectHandle, rel string) string {
	t.Helper()
	id := ids.MustNew(f.Clock)
	at := clock.Format(f.Clock.Now())
	if _, err := handle.DB().Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash, file_state)
		 VALUES (?, 'note', ?, 'scratchpad', ?, ?, '', 'ok')`,
		id, rel, at, at); err != nil {
		t.Fatalf("insert note with path: %v", err)
	}
	return id
}

// addAttachment writes an attachment file under the note's attachment dir.
func addAttachment(t *testing.T, f *foundationtest.Fixture, projectID, noteID, name string, content []byte) {
	t.Helper()
	rel := f.Workspace.Paths().AttachmentsDir(projectID, noteID) + "/" + name
	abs := filepath.Join(f.Root, filepath.FromSlash(rel))
	writeTestFile(t, abs, content)
}

// addPaper seeds a global paper whose local PDF contains content, and records
// the matching "sha256:<hex>" hash the way SPEC-04 would.
func addPaper(t *testing.T, f *foundationtest.Fixture, key string, content []byte) string {
	t.Helper()
	id, err := f.SeedPaper(context.Background(), key)
	if err != nil {
		t.Fatalf("seed paper: %v", err)
	}
	writeTestFile(t, filepath.Join(f.Root, filepath.FromSlash(f.Workspace.Paths().PaperPDF(id))), content)
	sum := sha256.Sum256(content)
	if _, err := f.Global().DB().Exec(
		`UPDATE papers SET file_hash = ?, file_size = ? WHERE id = ?`,
		"sha256:"+hex.EncodeToString(sum[:]), int64(len(content)), id); err != nil {
		t.Fatalf("update paper: %v", err)
	}
	return id
}

// linkPaper inserts the project_papers row (the export reads it directly).
func linkPaper(t *testing.T, f *foundationtest.Fixture, handle *fworkspace.ProjectHandle, paperID string) {
	t.Helper()
	if _, err := handle.DB().Exec(
		`INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by) VALUES (?, ?, ?, 'user')`,
		handle.ID, paperID, clock.Format(f.Clock.Now())); err != nil {
		t.Fatalf("link paper: %v", err)
	}
}

// addActivity writes one Activity row through the foundation writer.
func addActivity(t *testing.T, handle *fworkspace.ProjectHandle) {
	t.Helper()
	if err := handle.Events().EmitStandalone(context.Background(), events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityProject,
		EntityID:   handle.ID,
		Summary:    "seed",
		Actor:      schema.ActorUser,
	}); err != nil {
		t.Fatalf("emit activity: %v", err)
	}
}

// addRelation writes one entity_relations row (export must snapshot it).
func addRelation(t *testing.T, f *foundationtest.Fixture, handle *fworkspace.ProjectHandle) {
	t.Helper()
	noteID := ids.MustNew(f.Clock)
	paperID := ids.MustNew(f.Clock)
	if _, err := handle.DB().Exec(
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES (?, 'note', ?, 'references', 'paper', ?, ?)`,
		ids.MustNew(f.Clock), noteID, paperID, clock.Format(f.Clock.Now())); err != nil {
		t.Fatalf("insert relation: %v", err)
	}
}

func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// readBundle extracts the logical content of an exported bundle. Keys are
// bundle-relative paths with '/'; archives lose their "<base>/" prefix.
func readBundle(t *testing.T, format Format, output, base string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	switch format {
	case FormatFolder:
		err := filepath.WalkDir(output, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(output, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			t.Fatalf("walk folder bundle: %v", err)
		}
	case FormatZip:
		zr, err := zip.OpenReader(output)
		if err != nil {
			t.Fatalf("open zip: %v", err)
		}
		defer zr.Close()
		for _, file := range zr.File {
			name := trimBase(file.Name, base)
			rc, err := file.Open()
			if err != nil {
				t.Fatalf("open zip entry %s: %v", file.Name, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read zip entry %s: %v", file.Name, err)
			}
			out[name] = data
		}
	case FormatTarGz:
		file, err := os.Open(output)
		if err != nil {
			t.Fatalf("open tar.gz: %v", err)
		}
		defer file.Close()
		gz, err := gzip.NewReader(file)
		if err != nil {
			t.Fatalf("gzip: %v", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("tar next: %v", err)
			}
			if hdr.Typeflag != tar.TypeReg {
				continue
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read tar entry %s: %v", hdr.Name, err)
			}
			out[trimBase(hdr.Name, base)] = data
		}
	default:
		t.Fatalf("unknown format %q", format)
	}
	return out
}

func trimBase(name, base string) string {
	prefix := base + "/"
	if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return name
}

// sortedKeys returns the map keys in order.
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// decodeManifest parses manifest.json from an extracted bundle.
func decodeManifest(t *testing.T, bundle map[string][]byte) manifest {
	t.Helper()
	data, ok := bundle["manifest.json"]
	if !ok {
		t.Fatal("manifest.json missing from bundle")
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

// exportOnce plans and runs one export and returns the result.
func exportOnce(t *testing.T, s *Service, req Request) *Result {
	t.Helper()
	plan, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	jobID, err := s.Start(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := s.Await(context.Background(), jobID)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	return res
}
