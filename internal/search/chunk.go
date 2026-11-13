package search

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
)

const (
	// markdownMaxTokens and markdownOverlapTokens size Markdown chunks
	// (SPEC-10 §4.2: about 200–500 tokens with about 50 tokens overlap).
	markdownMaxTokens     = 500
	markdownOverlapTokens = 50
	// pdfMaxTokens bounds one PDF chunk; a longer page block is split inside
	// the page but never across pages.
	pdfMaxTokens = 500
)

// mdSegment is one Markdown building block (heading or paragraph) with its
// rune offsets in the original file.
type mdSegment struct {
	start   int
	end     int
	text    string
	heading string
	block   int
}

// chunkMarkdown splits Markdown into 200–500 token chunks on heading and
// paragraph boundaries with about 50 tokens of overlap. CharStart/CharEnd are
// rune offsets in the original Markdown, so overlap never distorts them
// (SPEC-10 §4.2).
func chunkMarkdown(docID, markdown string) []Chunk {
	runes := []rune(markdown)
	segments := splitMarkdownSegments(runes)
	if len(segments) == 0 {
		return nil
	}
	var chunks []Chunk
	add := func(text string, start, end int, seg mdSegment) {
		chunks = append(chunks, Chunk{
			ChunkID:     chunkID(docID, len(chunks)),
			DocID:       docID,
			Ordinal:     len(chunks),
			Text:        strings.TrimSpace(text),
			ContentHash: hashText(text),
			Locator: Locator{
				BlockNo:   seg.block,
				CharStart: start,
				CharEnd:   end,
				Heading:   seg.heading,
			},
		})
	}

	for i := 0; i < len(segments); {
		// An oversized single paragraph is split inside itself.
		if estimateTokens(segments[i].text) > markdownMaxTokens {
			splitLongSegment(runes, docID, segments[i], &chunks)
			i++
			continue
		}
		j := i
		tokens := 0
		for j < len(segments) {
			// A heading starts a new section: never merge across it, so the
			// heading path of a chunk stays exact (SPEC-10 §4.2).
			if j > i && isHeadingSegment(segments[j]) {
				break
			}
			t := estimateTokens(segments[j].text)
			if j > i && tokens+t > markdownMaxTokens {
				break
			}
			tokens += t
			j++
			if tokens >= markdownMaxTokens {
				break
			}
		}
		start := segments[i].start
		end := segments[j-1].end
		add(string(runes[start:end]), start, end, segments[i])

		next := j
		if next < len(segments) && !isHeadingSegment(segments[next]) {
			// Walk back until about 50 tokens of the closing context overlap.
			overlap := 0
			for k := j - 1; k > i; k-- {
				overlap += estimateTokens(segments[k].text)
				if overlap >= markdownOverlapTokens {
					next = k
					break
				}
			}
		}
		if next <= i {
			next = i + 1
		}
		i = next
	}
	return chunks
}

// isHeadingSegment reports whether a segment is a Markdown ATX heading.
func isHeadingSegment(seg mdSegment) bool {
	return strings.HasPrefix(strings.TrimSpace(seg.text), "#")
}

// splitLongSegment splits one over-long paragraph (or fenced code block) into
// rune windows with a small overlap, keeping original character offsets.
func splitLongSegment(runes []rune, docID string, seg mdSegment, chunks *[]Chunk) {
	const window = 1800
	const overlap = 180
	for start := seg.start; start < seg.end; {
		end := min(start+window, seg.end)
		*chunks = append(*chunks, Chunk{
			ChunkID:     chunkID(docID, len(*chunks)),
			DocID:       docID,
			Ordinal:     len(*chunks),
			Text:        strings.TrimSpace(string(runes[start:end])),
			ContentHash: hashText(string(runes[start:end])),
			Locator: Locator{
				BlockNo:   seg.block,
				CharStart: start,
				CharEnd:   end,
				Heading:   seg.heading,
			},
		})
		if end == seg.end {
			return
		}
		start = end - overlap
	}
}

// splitMarkdownSegments parses Markdown into heading/paragraph segments with
// rune offsets and the effective heading path.
func splitMarkdownSegments(runes []rune) []mdSegment {
	var (
		segments []mdSegment
		headings [6]string
		block    int
	)
	heading := func() string {
		var parts []string
		for _, h := range headings {
			if h != "" {
				parts = append(parts, h)
			}
		}
		return strings.Join(parts, " > ")
	}
	lineStart := 0
	inFence := false
	fenceStart := 0
	paragraphStart := -1

	flushParagraph := func(end int) {
		if paragraphStart < 0 {
			return
		}
		text := strings.TrimSpace(string(runes[paragraphStart:end]))
		if text != "" {
			segments = append(segments, mdSegment{
				start: paragraphStart, end: end, text: text,
				heading: heading(), block: block,
			})
			block++
		}
		paragraphStart = -1
	}

	for lineStart <= len(runes) {
		lineEnd := lineStart
		for lineEnd < len(runes) && runes[lineEnd] != '\n' {
			lineEnd++
		}
		line := string(runes[lineStart:lineEnd])
		trimmed := strings.TrimSpace(line)
		fence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		switch {
		case inFence:
			if fence {
				inFence = false
				flushParagraph(lineEnd)
			}
		case fence:
			flushParagraph(lineStart)
			inFence = true
			fenceStart = lineStart
			paragraphStart = fenceStart
		case trimmed == "":
			flushParagraph(lineStart)
		case isHeadingLine(trimmed):
			flushParagraph(lineStart)
			level, title := parseHeading(trimmed)
			headings[level-1] = title
			for i := level; i < len(headings); i++ {
				headings[i] = ""
			}
			text := strings.TrimSpace(line)
			segments = append(segments, mdSegment{
				start: lineStart, end: lineEnd, text: text,
				heading: heading(), block: block,
			})
			block++
		default:
			if paragraphStart < 0 {
				paragraphStart = lineStart
			}
		}
		if lineEnd >= len(runes) {
			break
		}
		lineStart = lineEnd + 1
	}
	flushParagraph(len(runes))
	if inFence && paragraphStart >= 0 {
		flushParagraph(len(runes))
	}
	return segments
}

// isHeadingLine reports whether the trimmed line is an ATX heading.
func isHeadingLine(trimmed string) bool {
	level, _ := parseHeadingLevel(trimmed)
	return level > 0
}

// parseHeading extracts the level and title of an ATX heading.
func parseHeading(trimmed string) (int, string) {
	level, rest := parseHeadingLevel(trimmed)
	return level, strings.TrimSpace(rest)
}

// parseHeadingLevel counts leading '#' characters (0 when not a heading).
func parseHeadingLevel(trimmed string) (int, string) {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0, trimmed
	}
	rest := trimmed[n:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, trimmed
	}
	return n, rest
}

// chunkPDFBlocks turns parsed PDF blocks into chunks. Blocks never span pages
// (SPEC-10 §4.2); a block longer than pdfMaxTokens is split inside its page.
func chunkPDFBlocks(docID string, blocks []PdfBlock) []Chunk {
	var chunks []Chunk
	for _, b := range blocks {
		runes := []rune(b.Text)
		if len(runes) == 0 {
			continue
		}
		if estimateTokens(b.Text) <= pdfMaxTokens {
			chunks = append(chunks, newPDFChunk(docID, len(chunks), b.Text, b, 0, len(runes)))
			continue
		}
		const window = 1800
		const overlap = 180
		for start := 0; start < len(runes); {
			end := min(start+window, len(runes))
			text := string(runes[start:end])
			chunks = append(chunks, newPDFChunk(docID, len(chunks), text, b, start, end))
			if end == len(runes) {
				break
			}
			start = end - overlap
		}
	}
	return chunks
}

// newPDFChunk builds one chunk for a slice of a parsed PDF block.
func newPDFChunk(docID string, ordinal int, text string, b PdfBlock, start, end int) Chunk {
	loc := Locator{
		PageNo:    b.PageNo,
		BlockNo:   b.BlockNo,
		CharStart: b.CharStart + start,
		CharEnd:   b.CharStart + end,
	}
	if b.Rects != nil {
		loc.Rects = b.Rects
	}
	return Chunk{
		ChunkID:     chunkID(docID, ordinal),
		DocID:       docID,
		Ordinal:     ordinal,
		Text:        strings.TrimSpace(text),
		ContentHash: hashText(text),
		Locator:     loc,
	}
}

// chunkID builds the stable chunk key.
func chunkID(docID string, ordinal int) string {
	return docID + "#" + strconv.Itoa(ordinal)
}

// estimateTokens approximates the token count: one per CJK rune, one per four
// other non-space runes. It only sizes chunks; retrieval quality never depends
// on exact tokenizer numbers.
func estimateTokens(s string) int {
	var cjk, other int
	for _, r := range s {
		switch {
		case isCJK(r):
			cjk++
		case unicode.IsSpace(r):
		default:
			other++
		}
	}
	return cjk + (other+3)/4
}

// isCJK reports whether r belongs to a CJK script.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// hashText returns the lowercase hex SHA-256 of s.
func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
