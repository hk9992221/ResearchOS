package formula

import "unicode/utf8"

// tokKind is what a token of TeX is.
type tokKind uint8

const (
	tokChar     tokKind = iota // a character of math or text
	tokCmd                     // a control sequence, its name without the backslash
	tokSpace                   // spaces, which \text keeps
	tokNBSP                    // ~, a space in text and a thin one in math
	tokLBrace                  // {
	tokRBrace                  // }
	tokSup                     // ^
	tokSub                     // _
	tokAmp                     // &
	tokLBracket                // [
	tokRBracket                // ]
	tokNewline                 // \\
)

// token is one piece of TeX source: a character, a command, or punctuation
// of its own.
type token struct {
	kind tokKind
	text string
}

// lex reads TeX into tokens. Comments run to the end of their line, spaces
// become one token each run of them, and a tilde counts as a space.
func lex(src string) []token {
	out := make([]token, 0, len(src)/2+4)
	for i := 0; i < len(src); {
		r, n := utf8.DecodeRuneInString(src[i:])
		switch r {
		case '%':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case ' ':
			for i < len(src) {
				c := src[i]
				if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
					break
				}
				i++
			}
			out = append(out, token{kind: tokSpace, text: " "})
			continue
		case '\t', '\r', '\n':
			i++
			out = append(out, token{kind: tokSpace, text: " "})
			continue
		case '~':
			i += n
			out = append(out, token{kind: tokNBSP, text: " "})
			continue
		case '\\':
			i += n
			if i >= len(src) {
				break
			}
			r2, n2 := utf8.DecodeRuneInString(src[i:])
			if isASCIILetter(r2) {
				start := i
				for i < len(src) && isASCIILetter(rune(src[i])) {
					i++
				}
				out = append(out, token{kind: tokCmd, text: src[start:i]})
				continue
			}
			i += n2
			switch r2 {
			case '\\':
				out = append(out, token{kind: tokNewline})
			case '{', '}', '%', '$', '&', '_', '#':
				// An escaped character stands for itself.
				out = append(out, token{kind: tokChar, text: string(r2)})
			default:
				out = append(out, token{kind: tokCmd, text: string(r2)})
			}
			continue
		case '{':
			out = append(out, token{kind: tokLBrace})
		case '}':
			out = append(out, token{kind: tokRBrace})
		case '^':
			out = append(out, token{kind: tokSup})
		case '_':
			out = append(out, token{kind: tokSub})
		case '&':
			out = append(out, token{kind: tokAmp})
		case '[':
			out = append(out, token{kind: tokLBracket})
		case ']':
			out = append(out, token{kind: tokRBracket})
		case '$':
			// Delimiters of inline math, which the caller may leave in.
		default:
			out = append(out, token{kind: tokChar, text: string(r)})
		}
		i += n
	}
	return out
}

func isASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
