// Package i18n translates the interface: the language packs, the current
// language and the fallbacks between them. See doc.go for the pack layout and
// how to add a language.
package i18n

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync/atomic"

	xlang "golang.org/x/text/language"
)

// packFS holds the language packs: one directory per language, named by its
// BCP 47 tag, and one JSON file per area of the interface inside it.
//
//go:embed locales/*/*.json
var packFS embed.FS

// defaultTag is the language every unsupported locale resolves to and the
// last step of the fallback chain.
const defaultTag = "en"

// systemTag is the configuration value that means "follow the operating
// system".
const systemTag = "system"

// Option is one selectable interface language.
type Option struct {
	// Tag is the pack's BCP 47 language tag, which is also its directory
	// name, as "en" or "zh". SetLanguage and AppConfig.Language store it.
	Tag string
	// NativeName is the language's own name for itself, read from the
	// pack's "language.name" key, as "简体中文".
	NativeName string
}

// catalog is one language pack loaded into memory. dict is written once at
// init and read-only afterwards, so T reads it without a lock.
type catalog struct {
	tag  string
	dict map[string]string
}

var (
	// english is the pack of defaultTag. It is loaded in every build because
	// the fallback chain ends there.
	english *catalog
	// catalogs indexes the packs by tag, which is the directory name.
	catalogs map[string]*catalog
	// options is the display order of Available: English first, then the
	// other tags alphabetically.
	options []Option
	// matcher maps a locale tag to the pack that serves it.
	matcher xlang.Matcher
	// active is the pack T translates with. SetLanguage swaps the pointer
	// atomically; readers never take a lock.
	active atomic.Pointer[catalog]
)

// SystemLocale reports the operating system's preferred locale, as "zh-CN" or
// "en_US.UTF-8". The platform detector fills it; tests and verification
// programs may replace it to force a locale.
var SystemLocale = detectLocale

func init() {
	entries, err := fs.ReadDir(packFS, "locales")
	if err != nil {
		panic("i18n: read embedded language packs: " + err.Error())
	}
	catalogs = make(map[string]*catalog, len(entries))
	var tags []string
	for _, e := range entries {
		if e.IsDir() {
			tags = append(tags, e.Name())
		}
	}
	if len(tags) == 0 {
		panic("i18n: no language packs are embedded under locales/")
	}
	sort.Strings(tags)
	for _, tag := range tags {
		catalogs[tag] = loadPack(tag)
	}

	english = catalogs[defaultTag]
	if english == nil {
		panic("i18n: the en language pack is required: it is the fallback for every other language")
	}

	ordered := orderTags(tags)
	options = make([]Option, 0, len(ordered))
	matcherTags := make([]xlang.Tag, 0, len(ordered))
	for _, tag := range ordered {
		c := catalogs[tag]
		name := c.dict["language.name"]
		if name == "" {
			name = tag // a pack may omit it; the selector then shows the tag.
		}
		options = append(options, Option{Tag: tag, NativeName: name})
		parsed, err := xlang.Parse(tag)
		if err != nil {
			panic(fmt.Sprintf("i18n: language pack directory %q is not a BCP 47 tag: %v", tag, err))
		}
		matcherTags = append(matcherTags, parsed)
	}
	matcher = xlang.NewMatcher(matcherTags)

	active.Store(english)
	SetLanguage("")
}

// orderTags puts English first and sorts the rest alphabetically.
func orderTags(tags []string) []string {
	ordered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == defaultTag {
			ordered = append(ordered, tag)
		}
	}
	for _, tag := range tags {
		if tag != defaultTag {
			ordered = append(ordered, tag)
		}
	}
	return ordered
}

// loadPack reads every area file of one language. A duplicate key across the
// files of one language, or a file that is not a flat map of strings, is a
// programming error in the build and panics with the file that holds it.
func loadPack(tag string) *catalog {
	dir := path.Join("locales", tag)
	entries, err := fs.ReadDir(packFS, dir)
	if err != nil {
		panic(fmt.Sprintf("i18n: read language pack %q: %v", tag, err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	c := &catalog{tag: tag, dict: make(map[string]string)}
	for _, name := range names {
		file := path.Join(dir, name)
		body, err := packFS.ReadFile(file)
		if err != nil {
			panic(fmt.Sprintf("i18n: read %s: %v", file, err))
		}
		area, err := decodeArea(body)
		if err != nil {
			panic(fmt.Sprintf("i18n: %s is not a flat JSON object of strings: %v", file, err))
		}
		for key, text := range area {
			if strings.TrimSpace(key) == "" {
				panic(fmt.Sprintf("i18n: %s holds an empty key", file))
			}
			if _, dup := c.dict[key]; dup {
				panic(fmt.Sprintf("i18n: duplicate key %q in %s: another file of the %q pack already defines it", key, file, tag))
			}
			c.dict[key] = text
		}
	}
	return c
}

// decodeArea parses one area file: a flat JSON object of string values. A key
// repeated inside the file is an error too, which encoding/json alone would
// hide by keeping the last value.
func decodeArea(body []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	open, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("the file must hold one JSON object")
	}
	area := make(map[string]string)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("a key is not a string")
		}
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("key %q: %w", key, err)
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("key %q: the value is not a string", key)
		}
		if _, dup := area[key]; dup {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		area[key] = text
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errors.New("the object is not closed")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after the object")
	}
	return area, nil
}

// T returns the text of key in the current language. A key the current pack
// does not define falls back to English, then to the key itself, so a missing
// translation never blanks a label and never panics. It is one map lookup on
// an atomically loaded pack, safe for concurrent use.
func T(key string) string {
	c := active.Load()
	if c != nil {
		if text, ok := c.dict[key]; ok {
			return text
		}
	}
	if c == nil || c.tag != defaultTag {
		if text, ok := english.dict[key]; ok {
			return text
		}
	}
	return key
}

// Tf is T with {name} placeholders replaced from vars. A placeholder without
// a value stays in the text, so an omission is visible instead of silently
// blank.
func Tf(key string, vars map[string]string) string {
	text := T(key)
	if len(vars) == 0 {
		return text
	}
	for name, value := range vars {
		text = strings.ReplaceAll(text, "{"+name+"}", value)
	}
	return text
}

// SetLanguage switches the interface language. "" and "system" follow the
// operating system; a supported tag ("en", "zh") selects that pack; any other
// value resolves from the operating system as well, so a configuration
// written by a newer build never leaves the interface blank. Safe for
// concurrent use with T and Tf.
func SetLanguage(lang string) {
	tag := ""
	normalized := normalizeLocale(lang)
	switch strings.ToLower(normalized) {
	case "", systemTag:
		tag = SystemLanguage()
	default:
		if resolved, ok := lookup(normalized); ok {
			tag = resolved
		} else {
			tag = SystemLanguage()
		}
	}
	if c, ok := catalogs[tag]; ok {
		active.Store(c)
		return
	}
	active.Store(english)
}

// Language returns the resolved current language tag, as "en" or "zh".
func Language() string {
	if c := active.Load(); c != nil {
		return c.tag
	}
	return defaultTag
}

// Available returns the selectable languages: English first, then the others
// alphabetically by tag. The slice is a copy, so callers may keep it.
func Available() []Option {
	out := make([]Option, len(options))
	copy(out, options)
	return out
}

// SystemLanguage returns the pack tag the operating system asks for. The
// settings page shows it beside the "Follow system" choice; "system" resolves
// to it. An unsupported system language resolves to "en".
func SystemLanguage() string {
	if tag, ok := lookup(SystemLocale()); ok {
		return tag
	}
	return defaultTag
}

// lookup returns the pack that serves a locale or language preference such as
// "zh-CN", "zh_TW", "en_GB.UTF-8" or "zh-Hant". The match is by base
// language, so any regional or script variant selects its language's pack;
// that is why zh-TW and zh-Hant currently resolve to the Simplified pack.
func lookup(pref string) (string, bool) {
	pref = normalizeLocale(pref)
	if pref == "" {
		return "", false
	}
	if _, ok := catalogs[strings.ToLower(pref)]; ok {
		return strings.ToLower(pref), true
	}
	tag, err := xlang.Parse(pref)
	if err != nil {
		return "", false
	}
	if base, baseConf := tag.Base(); baseConf != xlang.No {
		if _, ok := catalogs[base.String()]; ok {
			return base.String(), true
		}
	}
	_, idx, conf := matcher.Match(tag)
	if conf == xlang.No || idx < 0 || idx >= len(options) {
		return "", false
	}
	return options[idx].Tag, true
}

// normalizeLocale strips the encoding and modifier suffixes of a POSIX locale
// and turns its underscore into a hyphen, so "en_US.UTF-8" becomes "en-US".
func normalizeLocale(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	return strings.ReplaceAll(s, "_", "-")
}
