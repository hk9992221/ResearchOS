// Package config stores the application configuration in the OS config
// directory (SPEC-01 §5.4). It holds the workspace root and the Zotero data
// directory only: never secrets, project lists or caches.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"researchos/internal/foundation/errs"
)

// ConfigFile is the file name inside the application config directory.
const ConfigFile = "config.json"

// AppConfig is the on-disk configuration. SchemaVersion is the format version
// (currently 1).
type AppConfig struct {
	SchemaVersion int    `json:"schema_version"`
	WorkspaceRoot string `json:"workspace_root"`
	ZoteroDataDir string `json:"zotero_data_dir"`
	// Language is the interface language chosen by the user: "" or "system"
	// follows the operating system, and any other value names a bundled
	// language pack ("en", "zh"). It lives in the application configuration
	// rather than in workspace settings because the first-run wizard, which
	// runs before a workspace is open, already has to show it.
	Language string `json:"language,omitempty"`
}

// Store reads and writes the configuration file.
type Store interface {
	// Load returns the configuration. A missing file is the zero value and a
	// nil error; invalid JSON is ErrConfigCorrupt plus a .corrupt-<ts> backup.
	Load(ctx context.Context) (AppConfig, error)
	// Save writes atomically (temp file + fsync + rename) with mode 0600.
	Save(ctx context.Context, c AppConfig) error
	// Path is the configuration file path.
	Path() string
}

// NewStore returns the platform location of the application config file:
// %AppData%\ResearchOS\config.json on Windows, Application Support/ResearchOS
// on macOS, and $XDG_CONFIG_HOME/researchos/config.json on Linux.
func NewStore() (Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("config: user config dir: %w", err)
	}
	return NewStoreAt(filepath.Join(dir, appDirName(), ConfigFile))
}

// NewStoreAt returns a Store for an explicit file path. It is the injection
// seam used by tests and by the workspace fixture.
func NewStoreAt(path string) (Store, error) {
	if path == "" {
		return nil, errors.New("config: empty config path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: absolutize path: %w", err)
	}
	return &fileStore{path: abs}, nil
}

// appDirName is the per-platform application directory name. Linux keeps the
// conventional lowercase name; Windows and macOS use the product name.
func appDirName() string {
	if runtime.GOOS == "linux" {
		return "researchos"
	}
	return "ResearchOS"
}

type fileStore struct {
	path string
	mu   sync.Mutex
}

// Path implements Store.
func (s *fileStore) Path() string { return s.path }

// Load implements Store.
func (s *fileStore) Load(ctx context.Context) (AppConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	body, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return AppConfig{}, nil
	}
	if err != nil {
		return AppConfig{}, fmt.Errorf("config: read %s: %w", s.path, err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		backup, renameErr := s.quarantine()
		if renameErr != nil {
			return AppConfig{}, fmt.Errorf("%w: %v (quarantine failed: %v)", errs.ErrConfigCorrupt, err, renameErr)
		}
		return AppConfig{}, fmt.Errorf("%w: %v (moved to %s)", errs.ErrConfigCorrupt, err, backup)
	}
	return cfg, nil
}

// Save implements Store. The temp file lives in the same directory so the
// rename is atomic on every supported platform.
func (s *fileStore) Save(ctx context.Context, c AppConfig) error {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = 1
	}
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("config: rename into place: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// quarantine renames a corrupt configuration file and returns the new path.
func (s *fileStore) quarantine() (string, error) {
	backup := fmt.Sprintf("%s.corrupt-%s", s.path, time.Now().UTC().Format("20060102-150405.000"))
	if err := os.Rename(s.path, backup); err != nil {
		return "", err
	}
	return backup, nil
}
