package storage

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// embeddedMigrations carries the baseline schema shipped with the binary.
//
//go:embed migrations
var embeddedMigrations embed.FS

// migrationNamePattern is the mandatory file naming rule (§4.8.1).
var migrationNamePattern = regexp.MustCompile(`^(\d{4})_([a-z][a-z0-9_]*)\.sql$`)

// Migration is one schema migration unit. Version is the 4-digit band value,
// Name the snake_case file stem, SQL the verbatim migration text.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Checksum returns the lowercase hex sha256 of the migration text.
func (m Migration) Checksum() string {
	sum := sha256.Sum256([]byte(m.SQL))
	return hex.EncodeToString(sum[:])
}

// LoadFS reads every *.sql file in dir, parses its version from the file name
// and returns the migrations ordered by version. It is the seam used by module
// packages and tests to register extra migrations without editing this package.
func LoadFS(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("storage: read migrations %s: %w", dir, err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m, err := loadOne(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sortMigrations(out)
	return out, nil
}

func loadOne(fsys fs.FS, name string) (Migration, error) {
	base := path.Base(name)
	match := migrationNamePattern.FindStringSubmatch(base)
	if match == nil {
		return Migration{}, fmt.Errorf("storage: migration %q must match <4 digits>_<snake_case>.sql", name)
	}
	version, err := parseVersion(match[1])
	if err != nil {
		return Migration{}, fmt.Errorf("storage: migration %q: %w", name, err)
	}
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Migration{}, fmt.Errorf("storage: read migration %q: %w", name, err)
	}
	return Migration{Version: version, Name: match[2], SQL: string(body)}, nil
}

func parseVersion(digits string) (int, error) {
	if len(digits) != 4 {
		return 0, fmt.Errorf("version must have exactly 4 digits, got %q", digits)
	}
	v := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("version %q is not numeric", digits)
		}
		v = v*10 + int(c-'0')
	}
	if v == 0 {
		return 0, errors.New("version 0 is reserved")
	}
	return v, nil
}

// migrationsFor returns the embedded migrations of kind plus the extra set,
// validates uniqueness and sorts them ascending.
func migrationsFor(kind Kind, extra []Migration) ([]Migration, error) {
	dir := "migrations/" + string(kind)
	switch kind {
	case KindGlobal, KindProject:
	default:
		return nil, fmt.Errorf("storage: unknown migration kind %q", kind)
	}
	base, err := LoadFS(embeddedMigrations, dir)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]string, len(base)+len(extra))
	out := make([]Migration, 0, len(base)+len(extra))
	for _, m := range append(base, extra...) {
		if err := validateMigration(m); err != nil {
			return nil, err
		}
		if prev, dup := seen[m.Version]; dup {
			return nil, fmt.Errorf("storage: duplicate migration version %04d (%s and %s)", m.Version, prev, m.Name)
		}
		seen[m.Version] = m.Name
		out = append(out, m)
	}
	sortMigrations(out)
	return out, nil
}

func validateMigration(m Migration) error {
	if m.Version <= 0 || m.Version > 9999 {
		return fmt.Errorf("storage: migration %q has out-of-range version %d", m.Name, m.Version)
	}
	if m.Name == "" {
		return fmt.Errorf("storage: migration version %04d has no name", m.Version)
	}
	if strings.TrimSpace(m.SQL) == "" {
		return fmt.Errorf("storage: migration %04d_%s is empty", m.Version, m.Name)
	}
	return nil
}

func sortMigrations(ms []Migration) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
}

// TargetVersion returns the version this build migrates the given database kind
// to (the highest embedded/extra version).
func TargetVersion(kind Kind, opt Options) (int, error) {
	ms, err := migrationsFor(kind, opt.Extra[kind])
	if err != nil {
		return 0, err
	}
	if len(ms) == 0 {
		return 0, nil
	}
	return ms[len(ms)-1].Version, nil
}
