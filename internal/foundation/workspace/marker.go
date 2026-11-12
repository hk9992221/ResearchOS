package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/paths"
)

// MarkerName is the workspace marker file name (SPEC-00 §1 v1.1, S-05).
const MarkerName = ".researchos"

// MarkerApp is the app field of a valid marker.
const MarkerApp = "researchos"

// MarkerSchemaVersion is the marker format version written by this build.
const MarkerSchemaVersion = 1

// Marker is the content of the .researchos file.
type Marker struct {
	App           string `json:"app"`
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
}

// MarkerState is the result of reading the marker file.
type MarkerState int

const (
	// MarkerPresent means the marker exists and parses correctly.
	MarkerPresent MarkerState = iota
	// MarkerMissing means there is no marker file.
	MarkerMissing
	// MarkerInvalid means the marker exists but is not a valid ResearchOS marker.
	MarkerInvalid
)

// String implements fmt.Stringer.
func (s MarkerState) String() string {
	switch s {
	case MarkerPresent:
		return "present"
	case MarkerMissing:
		return "missing"
	case MarkerInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("MarkerState(%d)", int(s))
	}
}

func markerPath(root string) string { return filepath.Join(root, MarkerName) }

// readMarker reads and validates the marker. I/O errors are returned; a file
// that exists but does not describe a ResearchOS workspace is MarkerInvalid
// without an error.
func readMarker(root string) (Marker, MarkerState, error) {
	body, err := os.ReadFile(markerPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return Marker{}, MarkerMissing, nil
	}
	if err != nil {
		return Marker{}, MarkerMissing, fmt.Errorf("%w: read marker: %v", errs.ErrWorkspaceCorrupt, err)
	}
	var m Marker
	if err := json.Unmarshal(body, &m); err != nil {
		return Marker{}, MarkerInvalid, nil
	}
	if m.App != MarkerApp || m.SchemaVersion != MarkerSchemaVersion {
		return Marker{}, MarkerInvalid, nil
	}
	if _, err := clock.Parse(m.CreatedAt); err != nil {
		return Marker{}, MarkerInvalid, nil
	}
	return m, MarkerPresent, nil
}

// writeMarker creates the marker. Only Init calls this; Open/Scan/Repoint never
// rewrite an existing marker.
func writeMarker(root string, c clock.Clock) error {
	m := Marker{
		App:           MarkerApp,
		SchemaVersion: MarkerSchemaVersion,
		CreatedAt:     clock.Format(c.Now()),
	}
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("workspace: encode marker: %w", err)
	}
	if err := os.WriteFile(markerPath(root), body, paths.FileMode); err != nil {
		return fmt.Errorf("workspace: write marker: %w", err)
	}
	if err := os.Chmod(markerPath(root), paths.FileMode); err != nil {
		return fmt.Errorf("workspace: chmod marker: %w", err)
	}
	return nil
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirEmpty reports whether root holds no meaningful entries. The two OS
// turds that Windows/macOS drop into every directory are ignored (§5.4).
func dirEmpty(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		switch e.Name() {
		case ".DS_Store", "desktop.ini":
			continue
		}
		return false, nil
	}
	return true, nil
}
