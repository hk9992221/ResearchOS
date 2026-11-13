package zotero

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/literature"
)

var fixedTime = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

func testSettings(t *testing.T) *foundationtest.Fixture {
	t.Helper()
	fixture, err := foundationtest.New(t.TempDir(), foundationtest.Options{
		Clock: &clock.Fixed{T: fixedTime},
	})
	if err != nil {
		t.Fatalf("foundationtest.New: %v", err)
	}
	t.Cleanup(func() { fixture.Close() })
	return fixture
}

// createDataDir builds a directory that passes Validate: zotero.sqlite with
// schema version 128 and a storage/ directory.
func createDataDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(dir, "storage"), 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "zotero.sqlite")))
	if err != nil {
		t.Fatalf("create sqlite: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 128`); err != nil {
		db.Close()
		t.Fatalf("set user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	return dir
}

func writePrefs(t *testing.T, appData, profile, body string) string {
	t.Helper()
	dir := filepath.Join(appData, "Zotero", "Zotero", "Profiles", profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir prefs: %v", err)
	}
	path := filepath.Join(dir, "prefs.js")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write prefs: %v", err)
	}
	return path
}

func prefsBody(useDataDir bool, dataDir string) string {
	use := "false"
	if useDataDir {
		use = "true"
	}
	return `user_pref("extensions.zotero.useDataDir", ` + use + ");\n" +
		`user_pref("extensions.zotero.dataDir", ` + jsonQuote(dataDir) + ");\n"
}

func jsonQuote(s string) string {
	// Go's %q is JSON-compatible for Windows paths because backslashes are
	// escaped the same way.
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func TestConnectionDetect(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	appData := filepath.Join(base, "appdata")
	defaultDir := createDataDir(t, home, "Zotero")
	prefsDir := createDataDir(t, base, "prefs-data")
	writePrefs(t, appData, "abc123.default", prefsBody(true, prefsDir))

	fixture := testSettings(t)
	conn := NewConnection(Options{
		Settings:        fixture.Settings(),
		Clock:           fixture.Clock,
		GOOS:            "windows",
		HomeDir:         home,
		AppDataDir:      appData,
		LocalAPIBaseURL: "http://127.0.0.1:1",
	})

	candidates, err := conn.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].Path != canonicalPath(prefsDir) || candidates[0].Source != literature.DetectionPrefs || !candidates[0].Valid {
		t.Errorf("prefs candidate = %+v", candidates[0])
	}
	if candidates[1].Path != canonicalPath(defaultDir) || candidates[1].Source != literature.DetectionDefault || !candidates[1].Valid {
		t.Errorf("default candidate = %+v", candidates[1])
	}
	if _, ok, _ := fixture.Settings().Get(context.Background(), settingDataDir); ok {
		t.Error("Detect wrote a setting")
	}

	// A manual invalid directory is listed first with its problems.
	missing := filepath.Join(base, "missing")
	if err := fixture.Settings().Set(context.Background(), nil, settingDataDir, missing); err != nil {
		t.Fatalf("set data dir: %v", err)
	}
	candidates, err = conn.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect with manual: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].Path != canonicalPath(missing) || candidates[0].Source != literature.DetectionUser ||
		candidates[0].Exists || candidates[0].Valid {
		t.Errorf("manual candidate = %+v", candidates[0])
	}
	if !containsString(candidates[0].Problems, "目录不存在") {
		t.Errorf("manual problems = %+v", candidates[0].Problems)
	}

	// Dedupe by canonical path: prefs pointing at the default keeps one entry.
	writePrefs(t, appData, "abc123.default", prefsBody(true, defaultDir))
	candidates, err = conn.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect dedupe: %v", err)
	}
	if len(candidates) != 2 { // invalid manual + deduped prefs/default
		t.Fatalf("dedupe candidates = %+v", candidates)
	}
	if candidates[1].Source != literature.DetectionPrefs {
		t.Errorf("dedupe source = %+v", candidates[1])
	}

	// useDataDir=false disables the prefs candidate.
	writePrefs(t, appData, "abc123.default", prefsBody(false, prefsDir))
	candidates, err = conn.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect disabled: %v", err)
	}
	if len(candidates) != 2 || candidates[1].Source != literature.DetectionDefault {
		t.Fatalf("disabled candidates = %+v", candidates)
	}
}

func TestConnectionDetectWithoutSettingsOrPrefs(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	createDataDir(t, home, "Zotero")
	conn := NewConnection(Options{
		GOOS:       "linux",
		HomeDir:    home,
		ConfigDir:  filepath.Join(base, "config"),
		AppDataDir: filepath.Join(base, "appdata"),
	})
	candidates, err := conn.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Source != literature.DetectionDefault || !candidates[0].Valid {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestConnectionValidate(t *testing.T) {
	conn := NewConnection(Options{GOOS: "windows", HomeDir: t.TempDir(), AppDataDir: t.TempDir()})
	ctx := context.Background()

	valid := createDataDir(t, t.TempDir(), "Zotero")
	zd, err := conn.Validate(ctx, valid)
	if err != nil {
		t.Fatalf("Validate valid: %v", err)
	}
	if !zd.Valid || zd.SchemaVersion != 128 || zd.Path != canonicalPath(valid) {
		t.Errorf("zd = %+v", zd)
	}
	if zd.SQLitePath != filepath.Join(canonicalPath(valid), "zotero.sqlite") ||
		zd.StorageDir != filepath.Join(canonicalPath(valid), "storage") {
		t.Errorf("paths = %+v", zd)
	}

	noStorage := t.TempDir()
	db, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(noStorage, "zotero.sqlite")))
	_, _ = db.Exec(`PRAGMA user_version = 128`)
	_ = db.Close()
	zd, err = conn.Validate(ctx, noStorage)
	if err != nil || !zd.Valid {
		t.Fatalf("no storage zd=%+v err=%v", zd, err)
	}
	if !containsString(zd.Problems, hintNoStorage) {
		t.Errorf("missing storage warning: %+v", zd.Problems)
	}

	configDir := t.TempDir()
	writeFile(t, filepath.Join(configDir, "prefs.js"), "")
	writeFile(t, filepath.Join(configDir, "profiles.ini"), "")
	zd, err = conn.Validate(ctx, configDir)
	if !errors.Is(err, literature.ErrZoteroDirInvalid) || zd.Valid {
		t.Fatalf("config dir zd=%+v err=%v", zd, err)
	}
	if !containsString(zd.Problems, hintConfigDir) {
		t.Errorf("config hint missing: %+v", zd.Problems)
	}

	installDir := t.TempDir()
	writeFile(t, filepath.Join(installDir, "zotero.exe"), "")
	zd, err = conn.Validate(ctx, installDir)
	if !errors.Is(err, literature.ErrZoteroDirInvalid) {
		t.Fatalf("install dir err = %v", err)
	}
	if !containsString(zd.Problems, hintInstallDir) {
		t.Errorf("install hint missing: %+v", zd.Problems)
	}

	if _, err := conn.Validate(ctx, filepath.Join(t.TempDir(), "nope")); !errors.Is(err, literature.ErrZoteroDirInvalid) {
		t.Errorf("missing dir err = %v", err)
	}
	filePath := filepath.Join(t.TempDir(), "file")
	writeFile(t, filePath, "x")
	if zd, err := conn.Validate(ctx, filePath); !errors.Is(err, literature.ErrZoteroDirInvalid) {
		t.Errorf("file err = %v", err)
	} else if !containsString(zd.Problems, "路径不是目录") {
		t.Errorf("file problems = %+v", zd.Problems)
	}
	if _, err := conn.Validate(ctx, "   "); !errors.Is(err, literature.ErrZoteroDirInvalid) {
		t.Errorf("empty err = %v", err)
	}
}

func TestConnectionValidateSchemaFallbacks(t *testing.T) {
	conn := NewConnection(Options{GOOS: "linux", HomeDir: t.TempDir()})
	ctx := context.Background()

	// Legacy version table with user_version left at zero.
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "storage"), 0o755)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "zotero.sqlite")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE version (schema TEXT, version INTEGER); INSERT INTO version VALUES ('zotero', 99)`); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	_ = db.Close()
	zd, err := conn.Validate(ctx, dir)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if zd.SchemaVersion != 99 {
		t.Errorf("schema = %d, want 99", zd.SchemaVersion)
	}

	// A readable file that is not a SQLite database is valid but warns.
	bad := t.TempDir()
	writeFile(t, filepath.Join(bad, "zotero.sqlite"), "not a database")
	zd, err = conn.Validate(ctx, bad)
	if err != nil || !zd.Valid {
		t.Fatalf("bad db zd=%+v err=%v", zd, err)
	}
	if !containsStringPrefix(zd.Problems, "无法识别 Zotero 数据库 schema 版本") {
		t.Errorf("bad schema warning = %+v", zd.Problems)
	}
}

func TestConnectionSetDataDir(t *testing.T) {
	fixture := testSettings(t)
	home := filepath.Join(t.TempDir(), "home")
	autoDir := createDataDir(t, home, "Zotero")
	conn := NewConnection(Options{
		Settings: fixture.Settings(),
		Clock:    fixture.Clock,
		GOOS:     "linux",
		HomeDir:  home,
	})
	ctx := context.Background()

	manual := createDataDir(t, t.TempDir(), "manual")
	zd, err := conn.SetDataDir(ctx, manual)
	if err != nil {
		t.Fatalf("SetDataDir: %v", err)
	}
	if !zd.Valid || zd.Source != literature.DetectionUser || zd.Path != canonicalPath(manual) {
		t.Errorf("zd = %+v", zd)
	}
	assertSetting(t, fixture, settingDataDir, canonicalPath(manual))
	assertSetting(t, fixture, settingDataDirSource, "user")
	assertSetting(t, fixture, settingLastVerifiedAt, clock.Format(fixedTime))

	invalid := t.TempDir()
	zd, err = conn.SetDataDir(ctx, invalid)
	if !errors.Is(err, literature.ErrZoteroDirInvalid) || zd.Valid {
		t.Fatalf("invalid set zd=%+v err=%v", zd, err)
	}
	if zd.Source != literature.DetectionUser {
		t.Errorf("invalid source = %q", zd.Source)
	}
	assertSetting(t, fixture, settingDataDir, canonicalPath(manual)) // unchanged

	zd, err = conn.SetDataDir(ctx, "")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if zd.Path != canonicalPath(autoDir) || zd.Source != literature.DetectionDefault {
		t.Errorf("auto zd = %+v", zd)
	}
	if _, ok, _ := fixture.Settings().Get(ctx, settingDataDir); ok {
		t.Error("data dir not cleared")
	}
	assertSetting(t, fixture, settingDataDirSource, "auto")

	fixtureHome := filepath.Join(t.TempDir(), "empty-home")
	if err := os.MkdirAll(fixtureHome, 0o755); err != nil {
		t.Fatal(err)
	}
	empty := NewConnection(Options{Settings: fixture.Settings(), Clock: fixture.Clock, GOOS: "linux", HomeDir: fixtureHome})
	if _, err := empty.SetDataDir(ctx, ""); !errors.Is(err, literature.ErrZoteroNotConfigured) {
		t.Errorf("empty auto err = %v", err)
	}
	noSettings := NewConnection(Options{GOOS: "linux", HomeDir: fixtureHome})
	if _, err := noSettings.SetDataDir(ctx, manual); err == nil {
		t.Error("expected error without settings store")
	}
}

func TestConnectionStatus(t *testing.T) {
	fixture := testSettings(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	autoDir := createDataDir(t, home, "Zotero")
	manualDir := createDataDir(t, base, "manual")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []any{}, 0)
	}))
	defer server.Close()

	conn := NewConnection(Options{
		Settings:        fixture.Settings(),
		Clock:           fixture.Clock,
		GOOS:            "linux",
		HomeDir:         home,
		LocalAPIBaseURL: server.URL,
	})
	ctx := context.Background()

	status, err := conn.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.DataDir == nil || status.DataDir.Path != canonicalPath(autoDir) || status.DataDir.Source != literature.DetectionDefault {
		t.Fatalf("auto status = %+v", status.DataDir)
	}
	if !status.LocalAPI.Available || status.LocalAPI.BaseURL != server.URL+"/api/users/0" {
		t.Errorf("local api = %+v", status.LocalAPI)
	}
	if !status.CheckedAt.Equal(fixedTime) {
		t.Errorf("CheckedAt = %v", status.CheckedAt)
	}

	if err := fixture.Settings().Set(ctx, nil, settingDataDir, manualDir); err != nil {
		t.Fatalf("set manual: %v", err)
	}
	status, err = conn.Status(ctx)
	if err != nil {
		t.Fatalf("Status manual: %v", err)
	}
	if status.DataDir == nil || status.DataDir.Path != canonicalPath(manualDir) || status.DataDir.Source != literature.DetectionUser {
		t.Errorf("manual status = %+v", status.DataDir)
	}

	missing := filepath.Join(base, "gone")
	if err := fixture.Settings().Set(ctx, nil, settingDataDir, missing); err != nil {
		t.Fatalf("set missing: %v", err)
	}
	status, err = conn.Status(ctx)
	if err != nil {
		t.Fatalf("Status missing: %v", err)
	}
	if status.DataDir == nil || status.DataDir.Valid || !containsString(status.DataDir.Problems, "目录不存在") {
		t.Errorf("invalid status dir = %+v", status.DataDir)
	}

	emptyFixture := testSettings(t)
	noAuto := NewConnection(Options{
		Settings:        emptyFixture.Settings(),
		Clock:           emptyFixture.Clock,
		GOOS:            "linux",
		HomeDir:         filepath.Join(base, "no-home"),
		LocalAPIBaseURL: server.URL,
	})
	status, err = noAuto.Status(ctx)
	if err != nil {
		t.Fatalf("Status none: %v", err)
	}
	if status.DataDir != nil {
		t.Errorf("expected no data dir, got %+v", status.DataDir)
	}
	if !status.LocalAPI.Available {
		t.Errorf("local API should still be probed: %+v", status.LocalAPI)
	}
}

func TestConnectionLocalAPIProbe(t *testing.T) {
	base := t.TempDir()
	fixture := testSettings(t)
	probeConn := func(url string) *Connection {
		return NewConnection(Options{
			Settings:        fixture.Settings(),
			Clock:           fixture.Clock,
			GOOS:            "linux",
			HomeDir:         filepath.Join(base, "home"),
			LocalAPIBaseURL: url,
		})
	}

	var gotPath, gotQuery string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Zotero-Version", "7.0.1")
		writeJSON(w, http.StatusOK, []any{}, 0)
	}))
	defer ok.Close()
	status, err := probeConn(ok.URL).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.LocalAPI.Available || status.LocalAPI.Version != "7.0.1" {
		t.Errorf("local api = %+v", status.LocalAPI)
	}
	if gotPath != "/api/users/0/collections" || gotQuery != "limit=1" {
		t.Errorf("probe request = %s?%s", gotPath, gotQuery)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]any{}, -1)
	}))
	defer denied.Close()
	status, err = probeConn(denied.URL).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.LocalAPI.Available {
		t.Errorf("403 probe available = %+v", status.LocalAPI)
	}
	if !strings.Contains(status.LocalAPI.Error, hintAPIDisabled) || !strings.Contains(status.LocalAPI.Error, hintAPIAllowApps) {
		t.Errorf("403 error = %q", status.LocalAPI.Error)
	}

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	refusedURL := refused.URL
	refused.Close()
	status, err = probeConn(refusedURL).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.LocalAPI.Available || !strings.Contains(status.LocalAPI.Error, hintZoteroClosed) {
		t.Errorf("refused = %+v", status.LocalAPI)
	}

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/elsewhere", http.StatusFound)
	}))
	defer redirect.Close()
	status, err = probeConn(redirect.URL).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.LocalAPI.Available {
		t.Errorf("off-host redirect followed: %+v", status.LocalAPI)
	}
}

func TestConnectionFullBaseAndDefaults(t *testing.T) {
	conn := NewConnection(Options{})
	if got := conn.usersBaseURL(); got != DefaultLocalAPIBaseURL+"/api/users/0" {
		t.Errorf("default base = %q", got)
	}
	full := NewConnection(Options{LocalAPIBaseURL: "http://127.0.0.1:23119/api/users/0/"})
	if got := full.usersBaseURL(); got != "http://127.0.0.1:23119/api/users/0" {
		t.Errorf("full base = %q", got)
	}
	api := NewConnection(Options{LocalAPIBaseURL: "http://127.0.0.1:23119/api/"})
	if got := api.usersBaseURL(); got != "http://127.0.0.1:23119/api/users/0" {
		t.Errorf("api base = %q", got)
	}
	users := NewConnection(Options{LocalAPIBaseURL: "http://127.0.0.1:23119/api/users"})
	if got := users.usersBaseURL(); got != "http://127.0.0.1:23119/api/users/0" {
		t.Errorf("users base = %q", got)
	}
}

func assertSetting(t *testing.T, fixture *foundationtest.Fixture, key, want string) {
	t.Helper()
	got, ok, err := fixture.Settings().Get(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("setting %s: ok=%v err=%v", key, ok, err)
	}
	if got != want {
		t.Errorf("setting %s = %q, want %q", key, got, want)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsStringPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func TestConnectionLinkedAttachmentBaseDir(t *testing.T) {
	base := t.TempDir()
	appData := filepath.Join(base, "appdata")
	home := filepath.Join(base, "home")
	linked := filepath.Join(base, "linked-base")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	writePrefs(t, appData, "p1", prefsBody(false, "")+
		`user_pref("extensions.zotero.baseAttachmentPath", `+jsonQuote(linked)+");\n")

	conn := NewConnection(Options{GOOS: "windows", HomeDir: home, AppDataDir: appData})
	if got := conn.LinkedAttachmentBaseDir(); got != canonicalPath(linked) {
		t.Errorf("linked base = %q, want %q", got, canonicalPath(linked))
	}
	override := filepath.Join(base, "override")
	withOverride := NewConnection(Options{
		GOOS: "windows", HomeDir: home, AppDataDir: appData, LinkedBaseDir: override,
	})
	if got := withOverride.LinkedAttachmentBaseDir(); got != canonicalPath(override) {
		t.Errorf("override = %q", got)
	}

	// Source propagates the discovered base to the SQLite reader.
	f := newZoteroFixture(t)
	sqlite := openFixtureReader(t, f)
	sqlite.LinkedBaseDir = ""
	NewSource(conn, sqlite, nil)
	if sqlite.LinkedBaseDir != canonicalPath(linked) {
		t.Errorf("source did not configure linked base: %q", sqlite.LinkedBaseDir)
	}
}
