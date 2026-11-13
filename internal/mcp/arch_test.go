package mcp

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDatabaseImports enforces SPEC-11 §8.8 item 31: the MCP protocol and
// host packages must not import a SQL package or a database driver, and must
// not reach into the SQL-backed store package.
func TestNoDatabaseImports(t *testing.T) {
	forbidden := []string{
		"database/sql",
		"modernc.org/sqlite",
		"github.com/mattn/go-sqlite3",
		"researchos/internal/mcpstore",
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			value := strings.Trim(imp.Path.Value, `"`)
			for _, banned := range forbidden {
				if value == banned || strings.HasPrefix(value, banned+"/") {
					t.Errorf("%s imports %s, which the MCP runtime must not depend on", path, value)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
