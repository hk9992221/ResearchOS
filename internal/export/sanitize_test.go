package export

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeBase(t *testing.T) {
	const id = "0192f3c1-1111-7000-8000-abcdefabcdef"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "My Project", "My-Project"},
		{"cjk", "跨工况异常检测", "跨工况异常检测"},
		{"empty", "", "project-0192f3c1"},
		{"only separators", " -_- ", "project-0192f3c1"},
		{"reserved upper", "CON", "_CON"},
		{"reserved lower", "con", "_con"},
		{"reserved com", "COM1", "_COM1"},
		{"collapse", "a---b", "a-b"},
		{"punctuation", "name!!!with$$$stuff", "name-with-stuff"},
		{"underscore kept", "data_2024-01", "data_2024-01"},
		{"slashes", "a/b\\c", "a-b-c"},
		{"trailing dash", "abc---", "abc"},
		{"leading punctuation", "###abc", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeBase(tc.raw, id); got != tc.want {
				t.Fatalf("sanitizeBase(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	t.Run("long name truncated to 80 runes", func(t *testing.T) {
		raw := strings.Repeat("长", 120)
		got := sanitizeBase(raw, id)
		if n := utf8.RuneCountInString(got); n != maxBaseRunes {
			t.Fatalf("got %d runes, want %d", n, maxBaseRunes)
		}
	})

	t.Run("truncation does not leave a trailing dash", func(t *testing.T) {
		raw := strings.Repeat("a", maxBaseRunes-1) + "---tail"
		got := sanitizeBase(raw, id)
		if strings.HasSuffix(got, "-") {
			t.Fatalf("got %q with trailing dash", got)
		}
	})
}
