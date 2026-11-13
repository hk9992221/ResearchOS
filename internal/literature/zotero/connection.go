package zotero

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/literature"
)

// DefaultLocalAPIBaseURL is Zotero 7's local API root. The user library
// endpoint is derived from it ("…/api/users/0"); a base that already contains
// "/api/users/<id>" is used as given.
const DefaultLocalAPIBaseURL = "http://127.0.0.1:23119"

// The manual Zotero data directory is not a workspace setting: it lives in the
// application configuration (SPEC-00 §1, SPEC-01 BASE-09, SPEC-04 §4.7), so the
// settings page and the detection below share exactly one value. §4.7's
// zotero.data_dir_source is derived from that value (a non-empty manual path
// means DetectionUser), and zotero.last_verified_at is reported by
// ZoteroConnectionStatus.CheckedAt, so neither needs its own key.
const (
	localAPIProbeTimeout = time.Second
)

// Strings shown in the settings UI (§5.1, §6.1). They are user-facing Chinese
// diagnostics; tests assert on them.
const (
	hintConfigDir    = "这是 Zotero 配置目录，不是数据目录"
	hintInstallDir   = "这是安装目录"
	hintNoStorage    = "缺少 storage 目录，附件路径无法解析"
	hintAPIDisabled  = "本地 API 未启用"
	hintAPIAllowApps = "在 Zotero 设置中允许其他应用通信"
	hintZoteroClosed = "Zotero 未运行"
)

// Options configures a Connection. Every field has a production default so
// that NewConnection(Options{}) works; the path overrides exist for
// deterministic tests and for unusual installations.
type Options struct {
	// AppConfig is the SPEC-01 application configuration store that holds the
	// manually specified Zotero directory (AppConfig.ZoteroDataDir). Nil
	// disables persistence: Detect/Status still work by auto-detection,
	// SetDataDir returns an error, and no manual directory is read.
	AppConfig config.Store
	// Clock supplies CheckedAt and the validation timestamp. Nil means clock.System.
	Clock clock.Clock
	// HTTPClient is the base client for local API probes. Its transport is
	// cloned (never mutated) and its timeout is replaced by the probe's 1s
	// deadline. Nil means a default transport.
	HTTPClient *http.Client
	// GOOS overrides runtime.GOOS ("windows", "darwin", "linux", …).
	GOOS string
	// HomeDir overrides the user home directory. On macOS/Linux it is also
	// the parent of the default "Zotero" data directory.
	HomeDir string
	// AppDataDir overrides %APPDATA% on Windows. Ignored on other systems.
	AppDataDir string
	// ConfigDir overrides the Zotero application-configuration directory,
	// i.e. the directory that contains the Profiles tree:
	//   windows: %APPDATA%\Zotero\Zotero
	//   darwin:  ~/Library/Application Support/Zotero
	//   linux:   ~/.zotero/zotero
	// When empty it is derived from AppDataDir/HomeDir and GOOS.
	ConfigDir string
	// LocalAPIBaseURL overrides the local API root. It may include
	// "/api/users/0" or stop at the port; the user-library path is appended
	// when missing. Empty means DefaultLocalAPIBaseURL.
	LocalAPIBaseURL string
	// Version is the application version reported in diagnostics. Empty
	// means unknown.
	Version string
	// LinkedBaseDir overrides Zotero's "Linked Attachment Base Directory".
	// Empty means read extensions.zotero.baseAttachmentPath from prefs.js.
	LinkedBaseDir string
}

// Connection implements literature.ZoteroConnectionService. It is safe for
// concurrent use; all state is immutable after construction.
type Connection struct {
	appConfig config.Store
	clock     clock.Clock
	hc        *http.Client
	goos      string
	home      string
	appData   string
	config    string
	apiBase   string
	version   string
	linked    string
}

var _ literature.ZoteroConnectionService = (*Connection)(nil)

// NewConnection resolves the platform defaults and returns the service.
func NewConnection(opts Options) *Connection {
	c := &Connection{
		appConfig: opts.AppConfig,
		clock:     opts.Clock,
		hc:        opts.HTTPClient,
		goos:      strings.ToLower(strings.TrimSpace(opts.GOOS)),
		home:      strings.TrimSpace(opts.HomeDir),
		appData:   strings.TrimSpace(opts.AppDataDir),
		config:    strings.TrimSpace(opts.ConfigDir),
		apiBase:   strings.TrimSpace(opts.LocalAPIBaseURL),
		version:   strings.TrimSpace(opts.Version),
		linked:    strings.TrimSpace(opts.LinkedBaseDir),
	}
	if c.clock == nil {
		c.clock = clock.System{}
	}
	if c.goos == "" {
		c.goos = runtime.GOOS
	}
	if c.home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.home = home
		}
	}
	if c.appData == "" && c.goos == "windows" {
		if v := os.Getenv("APPDATA"); v != "" {
			c.appData = v
		}
	}
	if c.config == "" {
		switch c.goos {
		case "windows":
			if c.appData != "" {
				c.config = filepath.Join(c.appData, "Zotero", "Zotero")
			}
		case "darwin":
			if c.home != "" {
				c.config = filepath.Join(c.home, "Library", "Application Support", "Zotero")
			}
		default:
			if c.home != "" {
				c.config = filepath.Join(c.home, ".zotero", "zotero")
			}
		}
	}
	if c.apiBase == "" {
		c.apiBase = DefaultLocalAPIBaseURL
	}
	return c
}

// Detect scans candidates in priority order and dedupes by canonical absolute
// path. It never writes settings and never creates directories. A manual
// directory that is invalid stays in the list with its problems so the UI can
// explain the failure instead of silently falling back.
func (c *Connection) Detect(ctx context.Context) ([]literature.ZoteroCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userDir, hasUser, err := c.userDataDir(ctx)
	if err != nil {
		return nil, err
	}
	var ordered []autoCandidate
	if hasUser {
		ordered = append(ordered, autoCandidate{path: userDir, source: literature.DetectionUser})
	}
	ordered = append(ordered, c.autoCandidates()...)

	seen := make(map[string]bool, len(ordered))
	out := make([]literature.ZoteroCandidate, 0, len(ordered))
	for _, cand := range ordered {
		path := canonicalPath(cand.path)
		if path == "" {
			continue
		}
		key := dedupeKey(path, c.goos)
		if seen[key] {
			continue
		}
		seen[key] = true
		zd, _ := c.Validate(ctx, path)
		out = append(out, literature.ZoteroCandidate{
			Path:     path,
			Source:   cand.source,
			Exists:   dirExists(path),
			Valid:    zd.Valid,
			Problems: zd.Problems,
		})
	}
	return out, nil
}

// Validate checks one directory without modifying it. The returned value is
// populated even when invalid; the error is ErrZoteroDirInvalid in that case.
func (c *Connection) Validate(ctx context.Context, dir string) (literature.ZoteroDataDir, error) {
	zd := literature.ZoteroDataDir{}
	raw := strings.TrimSpace(dir)
	if raw == "" {
		zd.Problems = []string{"路径为空"}
		return zd, literature.ErrZoteroDirInvalid
	}
	if err := ctx.Err(); err != nil {
		zd.Path = raw
		zd.Problems = []string{err.Error()}
		return zd, err
	}
	path := canonicalPath(raw)
	zd.Path = path
	zd.SQLitePath = filepath.Join(path, "zotero.sqlite")
	zd.StorageDir = filepath.Join(path, "storage")

	info, err := os.Stat(path)
	switch {
	case err != nil:
		if os.IsNotExist(err) {
			zd.Problems = append(zd.Problems, "目录不存在")
		} else {
			zd.Problems = append(zd.Problems, "无法访问目录："+err.Error())
		}
		return zd, literature.ErrZoteroDirInvalid
	case !info.IsDir():
		zd.Problems = append(zd.Problems, "路径不是目录")
		return zd, literature.ErrZoteroDirInvalid
	}

	sqliteOK := false
	if fi, err := os.Stat(zd.SQLitePath); err != nil {
		if os.IsNotExist(err) {
			zd.Problems = append(zd.Problems, "缺少 zotero.sqlite")
		} else {
			zd.Problems = append(zd.Problems, "无法访问 zotero.sqlite："+err.Error())
		}
	} else if !fi.Mode().IsRegular() {
		zd.Problems = append(zd.Problems, "zotero.sqlite 不是普通文件")
	} else if f, err := os.Open(zd.SQLitePath); err != nil {
		zd.Problems = append(zd.Problems, "zotero.sqlite 不可读："+err.Error())
	} else {
		f.Close()
		sqliteOK = true
	}

	if si, err := os.Stat(zd.StorageDir); err != nil || !si.IsDir() {
		zd.Problems = append(zd.Problems, hintNoStorage)
	}

	if !sqliteOK && looksLikeConfigDir(path) {
		zd.Problems = append(zd.Problems, hintConfigDir)
	}
	installDir := looksLikeInstallDir(path)
	if installDir {
		zd.Problems = append(zd.Problems, hintInstallDir)
	}

	if sqliteOK {
		if version, known, err := readZoteroSchemaVersion(ctx, zd.SQLitePath); err != nil {
			zd.Problems = append(zd.Problems, "无法识别 Zotero 数据库 schema 版本："+err.Error())
		} else if known {
			zd.SchemaVersion = version
		} else {
			zd.Problems = append(zd.Problems, "无法识别 Zotero 数据库 schema 版本")
		}
	}

	// An installation directory is never treated as a data directory.
	zd.Valid = sqliteOK && !installDir
	if !zd.Valid {
		return zd, literature.ErrZoteroDirInvalid
	}
	return zd, nil
}

// SetDataDir persists a manual directory after validation. Passing an empty
// (or whitespace-only) string clears the manual override, restores the "auto"
// source and returns the currently auto-detected directory, or
// ErrZoteroNotConfigured when no auto candidate is valid.
func (c *Connection) SetDataDir(ctx context.Context, dir string) (literature.ZoteroDataDir, error) {
	if err := ctx.Err(); err != nil {
		return literature.ZoteroDataDir{}, err
	}
	if c.appConfig == nil {
		return literature.ZoteroDataDir{}, fmt.Errorf("zotero: 应用配置未初始化")
	}
	if strings.TrimSpace(dir) == "" {
		if err := c.saveDataDir(ctx, ""); err != nil {
			return literature.ZoteroDataDir{}, err
		}
		for _, cand := range c.autoCandidates() {
			zd, err := c.Validate(ctx, cand.path)
			if err == nil && zd.Valid {
				zd.Source = cand.source
				return zd, nil
			}
		}
		return literature.ZoteroDataDir{}, literature.ErrZoteroNotConfigured
	}

	zd, err := c.Validate(ctx, dir)
	zd.Source = literature.DetectionUser
	if err != nil || !zd.Valid {
		if err == nil {
			err = literature.ErrZoteroDirInvalid
		}
		return zd, err
	}
	if err := c.saveDataDir(ctx, zd.Path); err != nil {
		return zd, err
	}
	return zd, nil
}

// Status reports the effective data directory (manual if configured, even when
// invalid) and the local API probe. It never returns an error when nothing is
// configured; only a settings read failure is fatal.
func (c *Connection) Status(ctx context.Context) (literature.ZoteroConnectionStatus, error) {
	st := literature.ZoteroConnectionStatus{CheckedAt: c.clock.Now()}
	if err := ctx.Err(); err != nil {
		return st, err
	}
	st.LocalAPI = c.probeLocalAPI(ctx)

	userDir, hasUser, err := c.userDataDir(ctx)
	if err != nil {
		return st, err
	}
	if hasUser {
		zd, _ := c.Validate(ctx, userDir)
		zd.Source = literature.DetectionUser
		st.DataDir = &zd
		return st, nil
	}
	for _, cand := range c.autoCandidates() {
		zd, err := c.Validate(ctx, cand.path)
		if err == nil && zd.Valid {
			zd.Source = cand.source
			st.DataDir = &zd
			break
		}
	}
	return st, nil
}

// LinkedAttachmentBaseDir returns Zotero's "Linked Attachment Base Directory":
// the explicit option when set, otherwise the first value found in the
// discovered prefs.js files. An empty result means linked relative attachments
// cannot be resolved (O-10).
func (c *Connection) LinkedAttachmentBaseDir() string {
	if c.linked != "" {
		return c.linked
	}
	for _, file := range prefsFiles(c.config, c.goos) {
		p := readPrefs(file)
		if p.Err != nil {
			continue
		}
		if dir, ok := p.prefString("extensions.zotero.baseAttachmentPath"); ok {
			return canonicalPath(dir)
		}
	}
	return ""
}

// usersBaseURL is the effective library endpoint. Both "…:23119" and
// "…:23119/api/users/0" inputs are accepted.
func (c *Connection) usersBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(c.apiBase), "/")
	lower := strings.ToLower(base)
	switch {
	case strings.Contains(lower, "/api/users/"):
		return base
	case strings.HasSuffix(lower, "/api/users"):
		return base + "/0"
	case strings.HasSuffix(lower, "/api"):
		return base + "/users/0"
	default:
		return base + "/api/users/0"
	}
}

// probeLocalAPI issues the single, read-only availability probe.
func (c *Connection) probeLocalAPI(ctx context.Context) literature.LocalAPIStatus {
	base := c.usersBaseURL()
	status := literature.LocalAPIStatus{BaseURL: base}

	pctx, cancel := context.WithTimeout(ctx, localAPIProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pctx, http.MethodGet, base+"/collections?limit=1", nil)
	if err != nil {
		status.Error = "本地 API 地址无效：" + err.Error()
		return status
	}
	req.Header.Set("Accept", "application/json")

	hc := c.httpClient(localAPIProbeTimeout)
	resp, err := hc.Do(req)
	if err != nil {
		if pctx.Err() != nil {
			status.Error = "本地 API 探测超时（" + hintZoteroClosed + "）"
		} else {
			status.Error = hintZoteroClosed + "：" + err.Error()
		}
		return status
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		status.Available = true
		status.Version = firstHeader(resp, "Zotero-Version", "X-Zotero-Version")
		if status.Version == "" {
			status.Version = c.version
		}
	case resp.StatusCode == http.StatusForbidden:
		status.Error = hintAPIDisabled + "：" + hintAPIAllowApps
	default:
		status.Error = fmt.Sprintf("本地 API 返回 HTTP %d", resp.StatusCode)
	}
	return status
}

// httpClient clones the configured client so the caller's transport is never
// mutated, disables proxies and refuses cross-host redirects.
func (c *Connection) httpClient(timeout time.Duration) *http.Client {
	src := c.hc
	if src == nil {
		src = &http.Client{}
	}
	clone := *src
	clone.Timeout = timeout
	switch tr := src.Transport.(type) {
	case *http.Transport:
		cp := tr.Clone()
		cp.Proxy = nil
		clone.Transport = cp
	case nil:
		clone.Transport = &http.Transport{Proxy: nil}
	}
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if isLoopbackHost(req.URL.Hostname()) {
			return nil
		}
		return http.ErrUseLastResponse
	}
	return &clone
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func firstHeader(resp *http.Response, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(resp.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// userDataDir returns the manually configured Zotero directory, if any. The
// value is read from the application configuration, which is also what the
// settings page writes: one value, two entry points (§4.7).
func (c *Connection) userDataDir(ctx context.Context) (string, bool, error) {
	if c.appConfig == nil {
		return "", false, nil
	}
	cfg, err := c.appConfig.Load(ctx)
	if err != nil {
		return "", false, err
	}
	v := strings.TrimSpace(cfg.ZoteroDataDir)
	return v, v != "", nil
}

// saveDataDir rewrites only the Zotero directory of the application
// configuration, so a concurrent change to another field (the language, the
// workspace root) is not overwritten with a stale value. An empty path clears
// the manual override.
func (c *Connection) saveDataDir(ctx context.Context, dir string) error {
	cfg, err := c.appConfig.Load(ctx)
	if err != nil {
		return err
	}
	cfg.ZoteroDataDir = strings.TrimSpace(dir)
	if err := c.appConfig.Save(ctx, cfg); err != nil {
		return fmt.Errorf("zotero: 保存 Zotero 数据目录: %w", err)
	}
	return nil
}

type autoCandidate struct {
	path   string
	source literature.DetectionSource
}

// autoCandidates lists prefs-derived and platform-default directories in the
// §5.1 priority order (manual settings are handled separately).
func (c *Connection) autoCandidates() []autoCandidate {
	var out []autoCandidate
	for _, file := range prefsFiles(c.config, c.goos) {
		p := readPrefs(file)
		if p.Err != nil {
			continue // unreadable prefs is a diagnostic, not an error
		}
		if use, ok := p.prefBool("extensions.zotero.useDataDir"); ok && !use {
			continue
		}
		if dir, ok := p.prefString("extensions.zotero.dataDir"); ok {
			out = append(out, autoCandidate{path: dir, source: literature.DetectionPrefs})
		}
	}
	if def := c.defaultDataDir(); def != "" {
		out = append(out, autoCandidate{path: def, source: literature.DetectionDefault})
	}
	return out
}

func (c *Connection) defaultDataDir() string {
	if c.home == "" {
		return ""
	}
	return filepath.Join(c.home, "Zotero")
}

// canonicalPath makes p absolute and, when it exists, symlink-resolved.
func canonicalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

func dedupeKey(path, goos string) string {
	if goos == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func looksLikeConfigDir(path string) bool {
	return fileExists(filepath.Join(path, "prefs.js")) ||
		fileExists(filepath.Join(path, "profiles.ini"))
}

func looksLikeInstallDir(path string) bool {
	return fileExists(filepath.Join(path, "zotero.exe")) ||
		fileExists(filepath.Join(path, "Zotero.app")) ||
		fileExists(filepath.Join(path, "zotero"))
}
