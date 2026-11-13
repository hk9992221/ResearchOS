package export

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxBaseRunes caps the sanitized base name (§5.9.3).
const maxBaseRunes = 80

// reservedNames are the Windows device names that cannot be a file or
// directory name. Comparison is case-insensitive (§5.9.3).
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeBase turns raw into a safe file name: letters, digits, CJK and the
// two separators -/_ survive; everything else becomes '-'; runs of '-' collapse;
// both ends are trimmed of '-' and '_'; the result is cut to 80 runes. An empty
// result falls back to project-<id8> and a Windows device name gets a '_'
// prefix. The caller appends the -2..-100 conflict suffix afterwards; digits
// and '-' are already legal there.
func sanitizeBase(raw, projectID string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '-' {
			if lastDash {
				continue
			}
			b.WriteRune('-')
			lastDash = true
			continue
		}
		// Any other rune is a separator, but only between kept characters: a
		// leading separator would have to be trimmed again anyway.
		if b.Len() == 0 || lastDash {
			continue
		}
		b.WriteRune('-')
		lastDash = true
	}

	name := trimSeparators(b.String())
	if name == "" {
		name = fallbackBase(projectID)
	}
	name = truncateRunes(name, maxBaseRunes)
	name = trimSeparators(name)
	if name == "" {
		name = fallbackBase(projectID)
	}
	if reservedNames[strings.ToUpper(name)] {
		name = "_" + name
	}
	return name
}

// fallbackBase is the project-<id8> default of §5.9.3.
func fallbackBase(projectID string) string {
	id := projectID
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		id = "export"
	}
	return "project-" + id
}

// trimSeparators removes '-' and '_' from both ends.
func trimSeparators(s string) string { return strings.Trim(s, "-_") }

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
