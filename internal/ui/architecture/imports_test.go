// Package architecture enforces the UI's architecture boundary (AC-UI-09) by
// reading the import graph, declarations and calls of every non-test UI
// package. It fails with the offending file:line, which a build alone cannot
// catch.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the repository root from this package's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

type source struct {
	path string // relative to the repo root, slash separated
	pkg  string // import path of the package, e.g. researchos/internal/ui/pages
	file *ast.File
	fset *token.FileSet
}

// uiSources parses every non-test Go file of internal/ui/... and
// cmd/researchos.
func uiSources(t *testing.T) []source {
	t.Helper()
	root := repoRoot(t)
	var out []source
	dirs := []string{filepath.Join(root, "internal", "ui"), filepath.Join(root, "cmd", "researchos")}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			pkg := "researchos/" + strings.TrimPrefix(filepath.ToSlash(filepath.Dir(rel)), "/")
			if strings.HasPrefix(rel, "cmd/") {
				pkg = "researchos/" + filepath.ToSlash(filepath.Dir(rel))
			}
			out = append(out, source{path: rel, pkg: pkg, file: f, fset: fset})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no UI sources found")
	}
	return out
}

// pos formats a node position as path:line.
func pos(s source, n ast.Node) string {
	p := s.fset.Position(n.Pos())
	return p.String()
}

// TestNoDatabaseAccessInUI forbids SQL and the storage layer in UI code, and
// any sql.DB/sql.Tx in an exported field or signature.
func TestNoDatabaseAccessInUI(t *testing.T) {
	forbidden := map[string]string{
		"database/sql":                           "UI must not open SQLite or handle transactions",
		"modernc.org/sqlite":                     "SQLite driver belongs to foundation/storage",
		"github.com/mattn/go-sqlite3":            "SQLite driver belongs to foundation/storage",
		"researchos/internal/foundation/storage": "the database layer belongs to foundation services",
	}
	for _, s := range uiSources(t) {
		for _, imp := range s.file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := forbidden[path]; bad {
				t.Errorf("%s: imports %q: %s", s.path, path, why)
			}
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			switch d := n.(type) {
			case *ast.TypeSpec:
				st, ok := d.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, field := range st.Fields.List {
					if !hasExportedName(field) {
						continue
					}
					if sqlType(field.Type) {
						t.Errorf("%s: exported field %s of %s exposes sql.DB/sql.Tx", pos(s, field), fieldName(field), d.Name.Name)
					}
				}
			case *ast.FuncDecl:
				if !ast.IsExported(d.Name.Name) {
					return true
				}
				for _, fl := range []*ast.FieldList{d.Type.Params, d.Type.Results} {
					if fl == nil {
						continue
					}
					for _, field := range fl.List {
						if sqlType(field.Type) {
							t.Errorf("%s: exported function %s takes or returns sql.DB/sql.Tx", pos(s, field), d.Name.Name)
						}
					}
				}
			}
			return true
		})
	}
}

// TestNoWebViewOrWebStack forbids an embedded browser or a web UI stack.
func TestNoWebViewOrWebStack(t *testing.T) {
	forbiddenSubstrings := []string{
		"webview", "chromium", "chromedp", "/cef", "cefgo", "wails", "fyne.io", "gioui.org",
		"html/template", "net/http",
	}
	for _, s := range uiSources(t) {
		for _, imp := range s.file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbiddenSubstrings {
				if strings.Contains(strings.ToLower(path), bad) {
					t.Errorf("%s: imports %q: the UI is native and embeds no web view or web server", s.path, path)
				}
			}
		}
	}

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	gomod := string(body)
	for _, bad := range []string{"fyne.io", "gioui.org", "wails", "chromedp", "cef", "webview", "playwright", "go-rod"} {
		if strings.Contains(strings.ToLower(gomod), bad) {
			t.Errorf("go.mod requires a web or non-native UI stack matching %q", bad)
		}
	}
}

// TestNoDirectWorkspaceFileMutation forbids file mutation outside the
// foundation services. Read-only inspection (Stat, ReadFile, ReadDir) is
// allowed; the composition root (boot) alone may classify a candidate root
// with path arithmetic.
func TestNoDirectWorkspaceFileMutation(t *testing.T) {
	mutating := map[string]bool{
		"OpenFile": true, "Create": true, "WriteFile": true, "Remove": true,
		"RemoveAll": true, "Rename": true, "Mkdir": true, "MkdirAll": true,
		"Symlink": true, "Chmod": true, "Chown": true, "Truncate": true,
	}
	for _, s := range uiSources(t) {
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "os" && mutating[sel.Sel.Name] {
				t.Errorf("%s: calls os.%s: workspace files change only through foundation services", pos(s, call), sel.Sel.Name)
			}
			if pkg.Name == "filepath" && sel.Sel.Name == "Join" && !strings.HasPrefix(s.pkg, "researchos/internal/ui/boot") {
				t.Errorf("%s: joins workspace paths with filepath.Join: pages obtain paths from services", pos(s, call))
			}
			return true
		})
	}
}

// TestNoWorkspaceOpeningInPages checks that only the composition root opens or
// initializes a workspace; pages receive typed values through service
// interfaces.
func TestNoWorkspaceOpeningInPages(t *testing.T) {
	for _, s := range uiSources(t) {
		if strings.HasPrefix(s.pkg, "researchos/internal/ui/boot") {
			continue
		}
		for _, imp := range s.file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "researchos/internal/foundation/trash" {
				t.Errorf("%s: imports %q; the recycle bin is reached through the boot-supplied TrashAPI", s.path, path)
			}
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if (sel.Sel.Name == "Open" || sel.Sel.Name == "Init") &&
				(pkg.Name == "workspace" || pkg.Name == "fworkspace" || pkg.Name == "foundationworkspace") {
				t.Errorf("%s: opens a workspace directly; only the composition root does", pos(s, call))
			}
			return true
		})
	}
}

// hasExportedName reports whether the field declares an exported name.
func hasExportedName(field *ast.Field) bool {
	for _, n := range field.Names {
		if ast.IsExported(n.Name) {
			return true
		}
	}
	return false
}

// fieldName names a field for the failure message.
func fieldName(field *ast.Field) string {
	if len(field.Names) > 0 {
		return field.Names[0].Name
	}
	return "embedded"
}

// sqlType reports whether t is sql.DB, sql.Tx, *sql.DB or *sql.Tx.
func sqlType(t ast.Expr) bool {
	if star, ok := t.(*ast.StarExpr); ok {
		return sqlType(star.X)
	}
	sel, ok := t.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "sql" {
		return false
	}
	return sel.Sel.Name == "DB" || sel.Sel.Name == "Tx"
}
