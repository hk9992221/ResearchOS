package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/literature"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestFileStoreStoreCopiesAndHashes(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	paperID := ids.MustNew(f.Clock)
	content := bytes.Repeat([]byte("0123456789abcdef"), (1<<20)/16+8)

	stored, err := store.Store(context.Background(), paperID, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if want := f.Workspace.Paths().PaperPDF(paperID); stored.RelativePath != want {
		t.Errorf("relative path = %q, want %q", stored.RelativePath, want)
	}
	if want := sha256Hex(content); stored.Hash != want {
		t.Errorf("hash = %s, want %s", stored.Hash, want)
	}
	if stored.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", stored.Size, len(content))
	}

	abs, info, err := store.Stat(context.Background(), paperID)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(len(content)) {
		t.Errorf("stat size = %d, want %d", info.Size(), len(content))
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Error("copy content differs from the stream")
	}
	if names := dirEntries(t, filepath.Dir(abs)); len(names) != 1 || names[0] != "source.pdf" {
		t.Errorf("copy directory entries = %v, want [source.pdf]", names)
	}
}

func TestFileStoreStoreRefusesOverwrite(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	paperID := ids.MustNew(f.Clock)

	first := []byte("first copy")
	if _, err := store.Store(context.Background(), paperID, bytes.NewReader(first), int64(len(first))); err != nil {
		t.Fatalf("first store: %v", err)
	}
	_, err := store.Store(context.Background(), paperID, bytes.NewReader([]byte("second copy")), 0)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("second store error = %v, want os.ErrExist", err)
	}
	abs, _, err := store.Stat(context.Background(), paperID)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Errorf("copy was overwritten: %q", got)
	}
	if names := dirEntries(t, filepath.Dir(abs)); len(names) != 1 {
		t.Errorf("directory entries = %v, want only source.pdf", names)
	}
}

// failingReader returns data once, then err.
type failingReader struct {
	data []byte
	err  error
	sent bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, r.err
	}
	r.sent = true
	return copy(p, r.data), nil
}

func TestFileStoreStoreCleansUpOnReaderError(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	paperID := ids.MustNew(f.Clock)
	boom := errors.New("boom")

	_, err := store.Store(context.Background(), paperID,
		&failingReader{data: []byte("partial"), err: boom}, 0)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the reader error", err)
	}
	dir, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
	if err != nil {
		t.Fatalf("resolve dir: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("created directory was not cleaned up: %v", err)
	}
	if _, _, err := store.Stat(context.Background(), paperID); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("stat error = %v, want ErrPaperFileMissing", err)
	}
}

// cancelingReader cancels the context while returning its first chunk.
type cancelingReader struct {
	data     []byte
	cancel   context.CancelFunc
	sentData bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.sentData {
		return 0, io.EOF
	}
	r.sentData = true
	r.cancel()
	return copy(p, r.data), nil
}

func TestFileStoreStoreCleansUpOnCancel(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)

	t.Run("canceled before start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		paperID := ids.MustNew(f.Clock)
		_, err := store.Store(ctx, paperID, bytes.NewReader([]byte("x")), 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		dir, _ := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("canceled store created %s", dir)
		}
	})

	t.Run("cancel during copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		paperID := ids.MustNew(f.Clock)
		_, err := store.Store(ctx, paperID, &cancelingReader{
			data:   bytes.Repeat([]byte("y"), 4096),
			cancel: cancel,
		}, 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		dir, _ := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("canceled store left %s behind", dir)
		}
	})
}

func TestFileStoreStoreRejectsSizeMismatch(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	paperID := ids.MustNew(f.Clock)

	_, err := store.Store(context.Background(), paperID, bytes.NewReader([]byte("12345")), 6)
	if err == nil {
		t.Fatal("size mismatch was accepted")
	}
	dir, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
	if err != nil {
		t.Fatalf("resolve dir: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("mismatch left %s behind", dir)
	}
	if _, _, err := store.Stat(context.Background(), paperID); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("stat error = %v, want ErrPaperFileMissing", err)
	}
}

func TestFileStoreRejectsPathTraversal(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)

	if _, err := store.Store(context.Background(), "../escape", bytes.NewReader([]byte("x")), 0); err == nil {
		t.Fatal("traversal paper id was accepted")
	} else if !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatalf("error = %v, want ErrBadEntityID", err)
	}
	if _, _, err := store.Stat(context.Background(), "../../escape"); err == nil {
		t.Fatal("traversal stat was accepted")
	}
	if _, err := os.Stat(filepath.Join(f.Root, "library", "escape")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("traversal created data outside library/papers: %v", err)
	}
}

func TestFileStoreStatAndRemove(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	paperID := ids.MustNew(f.Clock)
	ctx := context.Background()

	if _, _, err := store.Stat(ctx, paperID); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Fatalf("stat missing = %v, want ErrPaperFileMissing", err)
	}
	if err := store.Remove(ctx, paperID); err != nil {
		t.Fatalf("remove missing = %v, want nil", err)
	}
	if _, err := store.Store(ctx, paperID, bytes.NewReader([]byte("copy")), 4); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Remove(ctx, paperID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, _, err := store.Stat(ctx, paperID); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("stat after remove = %v, want ErrPaperFileMissing", err)
	}
	dir, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
	if err != nil {
		t.Fatalf("resolve dir: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("paper directory still exists after remove: %v", err)
	}
	if err := store.Remove(ctx, paperID); err != nil {
		t.Errorf("second remove = %v, want nil", err)
	}
}

func TestFileStoreMove(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	ctx := context.Background()
	from := ids.MustNew(f.Clock)
	to := ids.MustNew(f.Clock)
	content := []byte("move me")
	if _, err := store.Store(ctx, from, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("store: %v", err)
	}

	if err := store.Move(ctx, from, to); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, _, err := store.Stat(ctx, from); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("source stat = %v, want ErrPaperFileMissing", err)
	}
	abs, _, err := store.Stat(ctx, to)
	if err != nil {
		t.Fatalf("target stat: %v", err)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("moved content = %q", got)
	}

	// An existing target is never overwritten.
	third := ids.MustNew(f.Clock)
	other := []byte("third")
	if _, err := store.Store(ctx, third, bytes.NewReader(other), int64(len(other))); err != nil {
		t.Fatalf("store third: %v", err)
	}
	if err := store.Move(ctx, third, to); !errors.Is(err, os.ErrExist) {
		t.Errorf("move onto existing target = %v, want os.ErrExist", err)
	}
	if abs, _, err := store.Stat(ctx, third); err != nil {
		t.Errorf("refused move removed the source: %v", err)
	} else if got, _ := os.ReadFile(abs); !bytes.Equal(got, other) {
		t.Errorf("source changed: %q", got)
	}

	// A missing source is an error, not a silent success.
	if err := store.Move(ctx, ids.MustNew(f.Clock), ids.MustNew(f.Clock)); err == nil {
		t.Error("missing source accepted")
	}
	if err := store.Move(ctx, "../escape", to); err == nil {
		t.Error("traversal source accepted")
	}
}

func TestFileStoreRemoveIgnoresCanceledContext(t *testing.T) {
	f := newFixture(t)
	store := NewFileStore(f.Workspace)
	paperID := ids.MustNew(f.Clock)
	if _, err := store.Store(context.Background(), paperID, bytes.NewReader([]byte("copy")), 4); err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Remove(ctx, paperID); err != nil {
		t.Fatalf("remove with canceled context: %v", err)
	}
	if _, _, err := store.Stat(context.Background(), paperID); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("copy survived canceled remove: %v", err)
	}
	dir, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
	if err != nil {
		t.Fatalf("resolve dir: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("directory survived canceled remove: %v", err)
	}
}
