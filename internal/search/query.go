package search

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// maxQueryRunes caps the accepted query length (SPEC-10 §5.1: 0~512 rune).
const maxQueryRunes = 512

// normalizedQuery is the query after the deterministic §5.2 step 1 pipeline.
type normalizedQuery struct {
	// Raw is the trimmed original query.
	Raw string
	// Text is the retrieval text: NFKC, full-width folded to half-width,
	// lowercased, quotes removed.
	Text string
	// Phrases are the quoted substrings; all of them must appear in a result.
	Phrases []string
	// Terms are the highlight tokens (>= 2 runes, deduplicated, lowercased).
	Terms []string
	// Lang is the detected language marker ("zh", "en" or "").
	Lang string
}

// normalizeQuery runs trim → NFKC → full/half-width fold → case fold →
// language detection → phrase extraction (SPEC-10 §5.2 step 1). Case folding
// is retrieval-only and never stored.
func normalizeQuery(raw string) (normalizedQuery, error) {
	q := normalizedQuery{Raw: strings.TrimSpace(raw)}
	if q.Raw == "" {
		return q, nil
	}
	if n := len([]rune(q.Raw)); n > maxQueryRunes {
		return normalizedQuery{}, Errorf(CodeInvalidRequest, "查询过长：%d 个字符（上限 %d）", n, maxQueryRunes)
	}
	folded := width.Fold.String(norm.NFKC.String(q.Raw))
	folded = strings.ToLower(folded)
	q.Text, q.Phrases = splitPhrases(folded)
	q.Text = strings.Join(strings.Fields(q.Text), " ")
	q.Lang = detectLang(q.Text + " " + strings.Join(q.Phrases, " "))
	q.Terms = highlightTerms(q.Text, q.Phrases)
	return q, nil
}

// splitPhrases removes quoted fragments (ASCII and CJK quotes) from s and
// returns them as must-conditions.
func splitPhrases(s string) (string, []string) {
	var (
		b       strings.Builder
		phrases []string
		open    rune
		start   int
		in      bool
	)
	runes := []rune(s)
	for i, r := range runes {
		if !in {
			if r == '"' || r == '\'' || r == '“' || r == '‘' || r == '「' {
				in = true
				open = r
				start = i + 1
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(r)
			continue
		}
		if closesPhrase(open, r) {
			if text := strings.TrimSpace(string(runes[start:i])); text != "" {
				phrases = append(phrases, text)
			}
			in = false
			b.WriteRune(' ')
			continue
		}
	}
	if in {
		// An unterminated quote is literal text, not a phrase.
		b.WriteString(string(runes[start:]))
	}
	return b.String(), phrases
}

// closesPhrase reports whether r closes the quote opened by open.
func closesPhrase(open, r rune) bool {
	switch open {
	case '“':
		return r == '”'
	case '‘':
		return r == '’'
	case '「':
		return r == '」'
	default:
		return r == open
	}
}

// detectLang classifies the retrieval text by script share.
func detectLang(text string) string {
	var cjk, latin int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			cjk++
		case unicode.IsLetter(r):
			latin++
		}
	}
	switch {
	case cjk == 0 && latin == 0:
		return ""
	case cjk > 0 && latin == 0:
		return "zh"
	case cjk*4 >= latin: // 20% CJK share already defeats word-based analysis
		return "zh"
	default:
		return "en"
	}
}

// highlightTerms extracts the term list used for snippet highlighting.
func highlightTerms(text string, phrases []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if len([]rune(v)) < 2 || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, p := range phrases {
		add(p)
	}
	for _, f := range strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		add(f)
	}
	return out
}

// retrievalText is the string sent to keyword/vector recall. A query made only
// of quoted phrases retrieves on those phrases and then enforces them as must
// conditions.
func (q normalizedQuery) retrievalText() string {
	if q.Text != "" {
		return q.Text
	}
	return strings.Join(q.Phrases, " ")
}

// containsPhrases reports whether every phrase occurs in text (both already
// case-folded). It implements the "quoted phrase = must" rule.
func containsPhrases(text string, phrases []string) bool {
	for _, p := range phrases {
		if !strings.Contains(text, p) {
			return false
		}
	}
	return true
}
