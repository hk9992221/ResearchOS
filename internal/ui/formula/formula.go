// Package formula typesets a subset of TeX as math for the Markdown
// renderer's math hooks: research notes' derivations, proofs and statistics.
//
// It is a drop-in for markdown.Options:
//
//	md := &markdown.Options{InlineMath: formula.Inline, Math: formula.Block}
//
// Inline lays out math within text as a baseline box the text makes room
// for, and Block lays out display math, centered. An input the package
// cannot lay out never loses its content and never panics: Inline shows its
// TeX source, as markdown does without the hook, and Block shows it as a
// monospace block with a Copy button.
package formula

import (
	"container/list"
	"math"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// Inline lays out math within text, for markdown.Options.InlineMath: a box
// Width wide, Height above the baseline and Depth below it, which Draw
// paints, or false for TeX this package does not set, which the renderer
// then shows as its source.
//
// The hook runs whenever a paragraph changes, so layouts are cached by their
// TeX and size.
func Inline(tex string, size float32) (markdown.InlineObject, bool) {
	src := source(tex)
	if src == "" || !usable(size) {
		return markdown.InlineObject{}, false
	}
	b, ok := layoutOf(src, size, false)
	if !ok {
		return markdown.InlineObject{}, false
	}
	return markdown.InlineObject{
		Width:  b.w,
		Height: b.h,
		Depth:  b.d,
		Draw: func(p *ui.Painter, r ui.Rect) {
			draw(p, r, b, p.Theme().Text)
		},
	}, true
}

// Block lays out display math from its TeX, for markdown.Options.Math:
// centered, with big operators at display size, in the window's text color.
// TeX it cannot lay out shows as a monospace block of its source, styled as
// a code block, with a button copying that source.
func Block(c *ui.Context, tex string) {
	th := c.Theme()
	size := th.FontSize
	if !usable(size) {
		size = 16
	}
	src := source(tex)
	b, ok := layoutOf(src, size, true)
	if !ok {
		sourceBlock(c, src)
		return
	}
	pad := 0.55 * size
	el := ui.Box(c).AlignSelf(ui.Stretch).Height(b.h + b.d + 2*pad)
	el.Role(ui.RoleImage).Label("Formula")
	el.Draw(func(p *ui.Painter, r ui.Rect) {
		color := p.Theme().Text
		at := ui.Rect{X: r.X, Y: r.Y + pad, W: b.w, H: b.h + b.d}
		if r.W <= 0 {
			return
		}
		if b.w <= r.W {
			// Centered, as display math is set.
			at.X = r.X + (r.W-b.w)/2
			draw(p, at, b, color)
			return
		}
		// A formula wider than the page shrinks to fit it, so that no part
		// of a wide matrix is lost on a narrow window.
		k := r.W / b.w
		p.Transform(ui.Scaling(k, k), r.X, r.Y+pad, func() {
			draw(p, at, b, color)
		})
	})
}

// draw paints a laid-out formula: glyphs on their baselines, fraction bars
// and rules as filled rectangles, and radicals and accents as strokes. The
// baseline of b sits b.h below the top of r.
func draw(p *ui.Painter, r ui.Rect, b *box, color ui.Color) {
	base := r.Y + b.h
	for i := range b.items {
		it := &b.items[i]
		switch it.kind {
		case itemGlyphs:
			p.Glyphs(it.glyphs, r.X+it.x, base-it.y, color)
		case itemRect:
			if it.w > 0 && it.h > 0 {
				p.Fill(ui.Rect{X: r.X + it.x, Y: base - it.y - it.h, W: it.w, H: it.h}, color, 0)
			}
		case itemStroke:
			var pts [6][2]float32
			n := min(int(it.n), len(pts))
			for k := 0; k < n; k++ {
				pts[k] = [2]float32{r.X + it.x + it.pts[k][0], base - (it.y + it.pts[k][1])}
			}
			p.StrokePolyline(pts[:n], it.th, color)
		}
	}
}

// sourceBlock shows TeX that could not be laid out, as a code block with a
// Copy button, so that nothing of it is lost.
func sourceBlock(c *ui.Context, tex string) {
	th := c.Theme()
	size := th.FontSize
	if !usable(size) {
		size = 16
	}
	code := max(size-2, 10)
	shell := ui.Column(c).Direction(ui.LeftToRight).
		Background(th.Surface).Border(1, th.Border).Radius(6).
		AlignSelf(ui.Stretch).Clip()
	shell.Role(ui.RoleGroup).Label("TeX formula")
	shell.Children(func() {
		head := ui.Row(c).AlignItems(ui.Center).Padding(4, 10, 0, 10)
		head.Children(func() {
			ui.Text(c, "TeX").FontSize(code).TextColor(th.TextMuted).Unselectable()
			ui.Spacer(c)
			btn := ui.ButtonBase(c).Padding(2, 8).Radius(4).Label("Copy TeX")
			btn.Children(func() {
				ui.Text(c, "Copy").FontSize(code).TextColor(th.TextMuted)
			})
			if btn.Clicked() {
				c.WriteClipboard(tex)
			}
		})
		ui.ScrollHorizontal(c).AlignSelf(ui.Stretch).Children(func() {
			ui.Text(c, tex).Font("monospace").FontSize(code).TextColor(th.TextMuted).
				NoWrap().Padding(0, 10, 8, 10)
		})
	})
}

// source is the TeX of a formula without the delimiters around it, which
// the Markdown renderer may leave in.
func source(tex string) string {
	s := strings.TrimSpace(tex)
	for {
		switch {
		case len(s) > 4 && strings.HasPrefix(s, "$$") && strings.HasSuffix(s, "$$"):
			s = strings.TrimSpace(s[2 : len(s)-2])
		case len(s) > 4 && strings.HasPrefix(s, `\[`) && strings.HasSuffix(s, `\]`):
			s = strings.TrimSpace(s[2 : len(s)-2])
		case len(s) > 4 && strings.HasPrefix(s, `\(`) && strings.HasSuffix(s, `\)`):
			s = strings.TrimSpace(s[2 : len(s)-2])
		case len(s) > 2 && strings.HasPrefix(s, "$") && strings.HasSuffix(s, "$"):
			s = strings.TrimSpace(s[1 : len(s)-1])
		default:
			return s
		}
	}
}

func usable(size float32) bool {
	return size > 0 && finite(size)
}

func finite(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
}

// cacheLimit bounds the layouts kept: a document shows far fewer formulas
// than that, and a stream of them ages the old ones out.
const cacheLimit = 512

type cacheKey struct {
	tex     string
	size    uint32
	display bool
}

type cacheEntry struct {
	key cacheKey
	box *box
	ok  bool
}

// layoutCache is the only mutable state of the package: laid-out formulas
// by their source and size, so that a paragraph redrawn each frame shapes
// its math once.
var layoutCache struct {
	mu sync.Mutex
	m  map[cacheKey]*list.Element
	l  *list.List
}

// layoutOf returns the layout of a formula, from the cache or by laying it
// out anew, and whether the package could lay it out.
func layoutOf(tex string, size float32, display bool) (*box, bool) {
	k := cacheKey{tex: tex, size: math.Float32bits(size), display: display}
	if b, ok, hit := cacheLookup(k); hit {
		return b, ok
	}
	b, ok := computeLayout(tex, size, display)
	cacheStore(k, b, ok)
	return b, ok
}

func cacheLookup(k cacheKey) (*box, bool, bool) {
	layoutCache.mu.Lock()
	defer layoutCache.mu.Unlock()
	if layoutCache.l == nil {
		return nil, false, false
	}
	el, ok := layoutCache.m[k]
	if !ok {
		return nil, false, false
	}
	layoutCache.l.MoveToFront(el)
	e := el.Value.(*cacheEntry)
	return e.box, e.ok, true
}

func cacheStore(k cacheKey, b *box, ok bool) {
	layoutCache.mu.Lock()
	defer layoutCache.mu.Unlock()
	if layoutCache.l == nil {
		layoutCache.l = list.New()
		layoutCache.m = make(map[cacheKey]*list.Element, cacheLimit)
	}
	if el, ok := layoutCache.m[k]; ok {
		e := el.Value.(*cacheEntry)
		e.box, e.ok = b, ok
		layoutCache.l.MoveToFront(el)
		return
	}
	el := layoutCache.l.PushFront(&cacheEntry{key: k, box: b, ok: ok})
	layoutCache.m[k] = el
	for layoutCache.l.Len() > cacheLimit {
		back := layoutCache.l.Back()
		if back == nil {
			break
		}
		layoutCache.l.Remove(back)
		delete(layoutCache.m, back.Value.(*cacheEntry).key)
	}
}

// computeLayout parses and lays out TeX, reporting false for input this
// package does not set. It never panics: math in a note is never worth
// taking the app down for.
func computeLayout(tex string, size float32, display bool) (b *box, ok bool) {
	defer func() {
		if recover() != nil {
			b, ok = nil, false
		}
	}()
	// Absurd input is not typeset, as no page shows it.
	if tex == "" || len(tex) > 4000 {
		return nil, false
	}
	g, err := parseTex(tex)
	if err != nil || len(g) == 0 {
		return nil, false
	}
	out := layoutTop(g, size, display)
	if out.w <= 0 || out.w > 1e5 || !finite(out.w) || !finite(out.h) || !finite(out.d) {
		return nil, false
	}
	return &out, true
}
