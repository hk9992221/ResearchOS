package zotero

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// prefLine matches the two shapes prefs.js uses for user preferences:
//
//	user_pref("extensions.zotero.dataDir", "F:\\Zotero");
//	pref("extensions.zotero.useDataDir", true);
var prefLine = regexp.MustCompile(`(?m)^\s*(?:user_)?pref\s*\(\s*"([^"]+)"\s*,\s*(.+?)\s*\)\s*;`)

// prefsFile describes one parsed prefs.js file. Files that cannot be read are
// reported through Err; callers treat them as diagnostics only, never as a
// failed detection.
type prefsFile struct {
	Path string
	Data map[string]any
	Err  error
}

// readPrefs parses every preference assignment in path. Values are decoded as
// JSON where possible (strings, booleans, numbers); anything else is kept as
// its raw text.
func readPrefs(path string) prefsFile {
	out := prefsFile{Path: path, Data: map[string]any{}}
	body, err := os.ReadFile(path)
	if err != nil {
		out.Err = err
		return out
	}
	text := strings.TrimPrefix(string(body), "\uFEFF")
	for _, m := range prefLine.FindAllStringSubmatch(text, -1) {
		name := m[1]
		raw := strings.TrimSpace(m[2])
		if raw == "" {
			continue
		}
		var value any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			// prefs.js is JavaScript, not JSON, but every value the detector
			// cares about is a JSON literal. Keep unknown shapes out.
			continue
		}
		out.Data[name] = value
	}
	return out
}

// prefString returns a string preference.
func (p prefsFile) prefString(name string) (string, bool) {
	v, ok := p.Data[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	return s, s != ""
}

// prefBool returns a boolean preference.
func (p prefsFile) prefBool(name string) (bool, bool) {
	v, ok := p.Data[name]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// prefsFiles returns the prefs.js files under the configured Zotero
// application directory, sorted for deterministic detection.
//
// Layouts (SPEC-04 §5.1, [待验证]):
//   - windows: <appDir>/Profiles/*/prefs.js
//   - darwin:  <appDir>/Profiles/*/prefs.js
//   - other:   <appDir>/*/prefs.js   (Linux: ~/.zotero/zotero/<profile>/)
func prefsFiles(appDir, goos string) []string {
	if strings.TrimSpace(appDir) == "" {
		return nil
	}
	var pattern string
	if goos == "windows" || goos == "darwin" {
		pattern = filepath.Join(appDir, "Profiles", "*", "prefs.js")
	} else {
		pattern = filepath.Join(appDir, "*", "prefs.js")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}
