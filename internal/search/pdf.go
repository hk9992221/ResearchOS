package search

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ParseOptions controls one PDF parse run (SPEC-10 §5.5). StartPage enables
// resuming a large document; MaxPage bounds how many pages are read.
type ParseOptions struct {
	StartPage int // 1-based; 0 or 1 means the first page
	MaxPage   int // 0 = all remaining pages
}

// PdfBlock is one page-local text block. PageNo is 1-based, BlockNo is 0-based
// inside the page, CharStart/CharEnd are rune offsets inside the page text.
type PdfBlock struct {
	PageNo    int
	BlockNo   int
	Text      string
	CharStart int
	CharEnd   int
	Rects     []Rect // optional; the v1 parser leaves them nil (SPEC-10 §5.5)
}

// PdfDocument is one parsed PDF.
type PdfDocument struct {
	PageCount int
	Blocks    []PdfBlock
}

// PDFParser is the pure-Go PDF text extraction port (SPEC-10 §5.5).
type PDFParser interface {
	Parse(ctx context.Context, absPath string, opts ParseOptions) (PdfDocument, error)
}

// GoPDFParser is the SPEC-10 default parser: pure Go, no cgo, page streaming
// (github.com/ledongthuc/pdf, chosen in T16). A scan-only PDF yields
// ErrNoTextLayer; OCR is out of scope (§10 O6).
type GoPDFParser struct{}

var _ PDFParser = GoPDFParser{}

// Parse extracts per-page text blocks. Pages are read in order and never
// merged across page boundaries; a malformed page fails with its page number
// so the caller can resume after it.
func (GoPDFParser) Parse(ctx context.Context, absPath string, opts ParseOptions) (PdfDocument, error) {
	if absPath == "" {
		return PdfDocument{}, Errorf(CodePDFUnreadable, "PDF 路径为空")
	}
	if err := ctx.Err(); err != nil {
		return PdfDocument{}, err
	}
	f, reader, err := pdf.Open(absPath)
	if err != nil {
		return PdfDocument{}, WrapError(CodePDFUnreadable, "无法打开 PDF", err)
	}
	defer f.Close()

	pageCount := reader.NumPage()
	if pageCount <= 0 {
		return PdfDocument{}, WrapError(CodeNoTextLayer, "PDF 无可读页面", ErrNoTextLayer)
	}
	start := opts.StartPage
	if start < 1 {
		start = 1
	}
	if start > pageCount {
		return PdfDocument{PageCount: pageCount}, nil
	}
	end := pageCount
	if opts.MaxPage > 0 && start+opts.MaxPage-1 < end {
		end = start + opts.MaxPage - 1
	}

	doc := PdfDocument{PageCount: pageCount}
	nonEmpty := false
	for pageNo := start; pageNo <= end; pageNo++ {
		if err := ctx.Err(); err != nil {
			return PdfDocument{}, err
		}
		blocks, err := parsePage(reader, pageNo)
		if err != nil {
			return PdfDocument{}, err
		}
		if len(blocks) > 0 {
			nonEmpty = true
		}
		doc.Blocks = append(doc.Blocks, blocks...)
	}
	if !nonEmpty {
		return doc, WrapError(CodeNoTextLayer, "PDF 没有文本层（可能是扫描件）", ErrNoTextLayer)
	}
	return doc, nil
}

// parsePage extracts the paragraph blocks of one page.
func parsePage(reader *pdf.Reader, pageNo int) (blocks []PdfBlock, err error) {
	defer func() {
		if r := recover(); r != nil {
			blocks = nil
			err = WrapError(CodePDFUnreadable, fmt.Sprintf("解析 PDF 第 %d 页失败", pageNo), fmt.Errorf("%v", r))
		}
	}()
	page := reader.Page(pageNo)
	if page.V.IsNull() {
		return nil, nil
	}
	content := page.Content()
	if len(content.Text) == 0 {
		return nil, nil
	}
	lines := groupGlyphLines(content.Text)
	var out []PdfBlock
	var offset int
	var paragraph []string
	var paragraphSize float64
	var lastY float64
	flush := func() {
		if len(paragraph) == 0 {
			return
		}
		text := strings.Join(paragraph, "\n")
		text = strings.TrimSpace(text)
		if text != "" {
			runes := []rune(text)
			out = append(out, PdfBlock{
				PageNo:    pageNo,
				BlockNo:   len(out),
				Text:      text,
				CharStart: offset,
				CharEnd:   offset + len(runes),
			})
			offset += len(runes) + 1 // page text joins blocks with "\n"
		}
		paragraph = nil
	}
	for _, line := range lines {
		if len(paragraph) > 0 {
			gap := lastY - line.y
			threshold := math.Max(paragraphSize, 1) * 1.9
			if gap > threshold {
				flush()
			}
		}
		if len(paragraph) == 0 {
			paragraphSize = line.size
		}
		paragraph = append(paragraph, line.text)
		lastY = line.y
	}
	flush()
	return out, nil
}

// glyph is one layout line of a page.
type glyphLine struct {
	y    float64
	size float64
	text string
}

// pageGlyph is one decoded character with its position.
type pageGlyph struct {
	x, y float64
	size float64
	s    string
}

// groupGlyphLines reconstructs layout lines from the character stream. Text
// pieces share a baseline; a vertical tolerance absorbs font rounding.
func groupGlyphLines(pieces []pdf.Text) []glyphLine {
	glyphs := make([]pageGlyph, 0, len(pieces))
	for _, p := range pieces {
		if p.S == "" {
			continue
		}
		glyphs = append(glyphs, pageGlyph{x: p.X, y: p.Y, size: p.FontSize, s: p.S})
	}
	if len(glyphs) == 0 {
		return nil
	}
	sort.SliceStable(glyphs, func(i, j int) bool {
		if math.Abs(glyphs[i].y-glyphs[j].y) > 2 {
			return glyphs[i].y > glyphs[j].y
		}
		return glyphs[i].x < glyphs[j].x
	})
	var (
		lines []glyphLine
		cur   []pageGlyph
	)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		sort.SliceStable(cur, func(i, j int) bool { return cur[i].x < cur[j].x })
		var b strings.Builder
		prevEnd := 0.0
		size := cur[0].size
		for i := range cur {
			g := cur[i]
			if i > 0 && g.x-prevEnd > math.Max(g.size, 4)*0.28 {
				if !strings.HasSuffix(b.String(), " ") {
					b.WriteByte(' ')
				}
			}
			b.WriteString(g.s)
			prevEnd = g.x + g.size*0.5
		}
		text := strings.TrimSpace(b.String())
		if text != "" {
			lines = append(lines, glyphLine{y: cur[0].y, size: size, text: text})
		}
		cur = nil
	}
	for _, g := range glyphs {
		if len(cur) > 0 && math.Abs(g.y-cur[0].y) > 2 {
			flush()
		}
		cur = append(cur, g)
	}
	flush()
	return lines
}

// ParsePDFFile is the default Config.ParsePDF used by the extractor. It reads
// the file read-only; the parser never writes the PDF or the Paper row.
func ParsePDFFile(ctx context.Context, absPath string, opts ParseOptions) (PdfDocument, error) {
	if _, err := os.Stat(absPath); err != nil {
		return PdfDocument{}, WrapError(CodePDFUnreadable, "读取 PDF 失败", err)
	}
	return GoPDFParser{}.Parse(ctx, absPath, opts)
}
