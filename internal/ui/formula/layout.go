package formula

import (
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/tokens"
)

// style is a TeX math style: how large its text is, how deeply nested in
// scripts, and whether it is display or inline.
type style struct {
	base    float32 // the size Inline or Block was asked for
	size    float32 // this style's size
	level   uint8   // 0 text or display, 1 script, 2 scriptscript
	display bool
	upright bool // letters stand upright, as \text and \mathrm set them
	italic  bool // letters lean, as math variables do
	weight  int
}

// styleSizes are the sizes of the three levels, in multiples of the base.
var styleSizes = [3]float32{1, 0.7, 0.5}

func newStyle(size float32, display bool) style {
	return style{base: size, size: size, display: display, italic: true}
}

// script is the style of a superscript or subscript.
func (s style) script() style {
	n := s.level + 1
	if n > 2 {
		n = 2
	}
	t := s
	t.level = n
	t.size = s.base * styleSizes[n]
	return t
}

// fracChild is the style of a fraction's numerator and denominator: text
// style below display, script style in text.
func (s style) fracChild() style {
	if s.display {
		return s
	}
	return s.script()
}

// with sets the face a run kind asks for.
func (s style) with(k runKind) style {
	switch k {
	case runBold:
		s.upright, s.italic, s.weight = true, false, 600
	case runItalic:
		s.upright, s.italic = false, true
	default:
		s.upright, s.italic = true, false
	}
	return s
}

// box is laid-out math: a run of items w wide, h above the baseline and d
// below it.
type box struct {
	w, h, d float32
	items   []item
}

// itemKind is what an item paints.
type itemKind uint8

const (
	itemGlyphs itemKind = iota
	itemRect
	itemStroke
)

// item is one thing a box paints, at (x, y) from the box's left edge and
// baseline, y growing upwards: a run of glyphs on a baseline, a filled
// rectangle whose bottom edge is at y, or a stroked polyline through points
// relative to (x, y).
type item struct {
	kind   itemKind
	x, y   float32
	w, h   float32
	th     float32
	glyphs []ui.Glyph
	pts    [6][2]float32
	n      uint8
}

// put adds another box's items, moved by (dx, dy).
func (b *box) put(o *box, dx, dy float32) {
	for k := range o.items {
		it := o.items[k]
		it.x += dx
		it.y += dy
		b.items = append(b.items, it)
	}
}

func (b *box) glyph(g []ui.Glyph, x, y float32) {
	if len(g) == 0 {
		return
	}
	b.items = append(b.items, item{kind: itemGlyphs, x: x, y: y, glyphs: g})
}

func (b *box) rect(x, y, w, h float32) {
	b.items = append(b.items, item{kind: itemRect, x: x, y: y, w: w, h: h})
}

func (b *box) stroke(x, y, th float32, pts [][2]float32) {
	if len(pts) < 2 {
		return
	}
	var it item
	it.kind, it.x, it.y, it.th = itemStroke, x, y, th
	it.n = uint8(min(len(pts), len(it.pts)))
	copy(it.pts[:], pts)
	b.items = append(b.items, it)
}

// runMetrics returns how far a run of text reaches above and below its
// baseline, in DIPs, from the shapes of its characters: ascenders, capitals
// and digits are tall, and descenders dip below.
func runMetrics(text string, size float32) (h, d float32) {
	const (
		cap  = 0.72
		xh   = 0.52
		desc = 0.22
	)
	up, down := float32(0.56), float32(0.06)
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			up = max(up, cap)
		case r >= 'a' && r <= 'z':
			switch {
			case strings.ContainsRune("bdfhklt", r):
				up = max(up, cap)
			case strings.ContainsRune("gjpqy", r):
				up = max(up, cap)
				down = max(down, desc)
			default:
				up = max(up, xh)
			}
		case strings.ContainsRune("()[]{}|‖/", r):
			up = max(up, 0.76)
			down = max(down, 0.24)
		case r > unicode.MaxASCII && unicode.IsUpper(r):
			up = max(up, cap)
		case r > unicode.MaxASCII && unicode.IsLetter(r):
			up = max(up, xh)
			if strings.ContainsRune("γζξηφ", r) {
				down = max(down, desc)
			}
		default:
			up = max(up, 0.56)
		}
	}
	return up * size, down * size
}

// shape shapes a string at a style's size, in the math face: TeX set in the
// interface sans reads as broken.
func shape(text string, s style, italic bool) ([]ui.Glyph, float32) {
	g := ui.Shape(text, ui.Font{Family: tokens.SystemFonts.Math, Size: s.size, Italic: italic, Weight: s.weight})
	if len(g) == 0 {
		return nil, 0.5 * s.size
	}
	return g, g[len(g)-1].X + g[len(g)-1].Advance
}

// firstRune is the first rune of s, for the tables that test one.
func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// gapEm is the space between two atoms of the given classes, in ems.
func gapEm(a, b class) float32 {
	const (
		thin  = 3.0 / 18
		med   = 4.0 / 18
		thick = 5.0 / 18
	)
	right := func(c class) float32 {
		switch c {
		case classBin:
			return med
		case classRel:
			return thick
		case classOp, classPunct, classInner:
			return thin
		}
		return 0
	}
	left := func(c class) float32 {
		switch c {
		case classBin:
			return med
		case classRel:
			return thick
		case classOp, classInner:
			return thin
		}
		return 0
	}
	return max(right(a), left(b))
}

// classOf is the class of a node, without laying it out.
func classOf(n node) class {
	switch v := n.(type) {
	case sym:
		return v.cls
	case bigop:
		return classOp
	case script:
		return classOf(v.base)
	}
	return classOrd
}

// layoutList lays out nodes in a row, with the space TeX puts between their
// classes.
func layoutList(g group, s style) box {
	classes := make([]class, len(g))
	for i, n := range g {
		classes[i] = classOf(n)
	}
	// A binary operator at the start or end of a list, or after an operator,
	// relation or opening, is ordinary, as TeX sets it.
	for i := range classes {
		if classes[i] != classBin || i == 0 || i == len(g)-1 {
			continue
		}
		if p := classes[i-1]; p != classOrd && p != classClose {
			classes[i] = classOrd
		}
	}
	var out box
	x := float32(0)
	prev := classOrd
	have, spaced := false, false
	for i, n := range g {
		if ss, ok := n.(styleSwitch); ok {
			s.display = ss.display > 0
			continue
		}
		if sp, ok := n.(spacing); ok {
			x += sp.em * s.size
			spaced = true
			continue
		}
		if have && !spaced {
			x += gapEm(prev, classes[i]) * s.size
		}
		b := layoutNode(n, s)
		if x < 0 {
			x = 0
		}
		out.put(&b, x, 0)
		x += b.w
		out.h = max(out.h, b.h)
		out.d = max(out.d, b.d)
		prev = classes[i]
		have, spaced = true, false
	}
	out.w = max(x, 0)
	return out
}

// layoutNode lays out one node in a style.
func layoutNode(n node, s style) box {
	switch v := n.(type) {
	case group:
		return layoutList(v, s)
	case sym:
		return layoutSym(v, s)
	case textRun:
		return layoutText(v, s)
	case styled:
		return layoutList(v.body, s.with(v.kind))
	case frac:
		return layoutFrac(v, s)
	case sqrt:
		return layoutSqrt(v, s)
	case script:
		return layoutScript(v, s)
	case bigop:
		return layoutBigOp(v, s)
	case delim:
		return layoutDelim(v, s)
	case accent:
		return layoutAccent(v, s)
	case envNode:
		return layoutEnv(v, s)
	case spacing:
		return box{w: max(v.em, 0) * s.size}
	}
	return box{}
}

func layoutSym(n sym, s style) box {
	g, w := shape(n.ch, s, s.italic && !s.upright && isVarRune(n.ch))
	h, d := runMetrics(n.ch, s.size)
	var b box
	b.w, b.h, b.d = w, h, d
	b.glyph(g, 0, 0)
	return b
}

func layoutText(n textRun, s style) box {
	st := s.with(n.kind)
	g, w := shape(n.text, st, st.italic && !st.upright)
	h, d := runMetrics(n.text, st.size)
	var b box
	b.h, b.d = h, d
	pad := float32(0)
	if n.op {
		pad = 0.12 * st.size
		w += 2 * pad
	}
	b.w = w
	b.glyph(g, pad, 0)
	return b
}

// ruleThickness is the thickness of fraction bars, radicals and rules.
func ruleThickness(size float32) float32 {
	return max(size/18, 1)
}

// axisHeight is where the bars of fractions sit, in ems.
const axisHeight = 0.25

func layoutFrac(n frac, s style) box {
	cs := s.fracChild()
	switch n.kind {
	case fracDisplay:
		cs = s
	case fracText:
		cs = s.script()
	}
	num := layoutList(n.num, cs)
	den := layoutList(n.den, cs)
	th := ruleThickness(s.size)
	axis := axisHeight * s.size
	gap := 0.12 * s.size
	nb := axis + th/2 + gap + num.d // the numerator's baseline
	db := axis - th/2 - gap - den.h // the denominator's baseline
	w := max(num.w, den.w) + 0.14*s.size
	var b box
	b.w = w
	b.h = nb + num.h
	b.d = den.d - db
	b.put(&num, (w-num.w)/2, nb)
	b.put(&den, (w-den.w)/2, db)
	b.rect(0.02*s.size, axis-th/2, w-0.04*s.size, th)
	return b
}

func layoutSqrt(n sqrt, s style) box {
	body := layoutList(n.body, s)
	th := ruleThickness(s.size)
	gap := 0.1 * s.size
	top := body.h + gap
	var idx box
	var iw float32
	if len(n.index) > 0 {
		idx = layoutList(n.index, s.script())
		iw = idx.w + 0.08*s.size
	}
	rw := 0.55 * s.size
	var b box
	b.w = iw + rw + body.w + 0.06*s.size
	b.h = top + th
	b.d = body.d
	b.put(&body, iw+rw, 0)
	if len(n.index) > 0 {
		y := top - 0.06*s.size
		b.put(&idx, 0, y)
		b.h = max(b.h, y+idx.h, top+th)
	}
	y0 := 0.45 * top
	b.stroke(0, 0, th, [][2]float32{
		{iw, y0},
		{iw + 0.2*rw, y0 - 0.22*s.size},
		{iw + 0.45*rw, top},
		{iw + rw + body.w + 0.06*s.size, top},
	})
	return b
}

func layoutBigOp(n bigop, s style) box {
	st := s
	if s.display {
		st.size = s.size * 1.4
	}
	st.upright, st.italic = true, false
	g, w := shape(n.ch, st, false)
	h, d := 0.8*st.size, 0.18*st.size
	if strings.ContainsRune("∫∬∭∮", firstRune(n.ch)) {
		h, d = 0.78*st.size, 0.28*st.size
	}
	var b box
	b.w, b.h, b.d = w, h, d
	b.glyph(g, 0, 0)
	return b
}

func layoutScript(n script, s style) box {
	base := layoutNode(n.base, s)
	ss := s.script()
	hasSup, hasSub := len(n.sup) > 0, len(n.sub) > 0
	var sup, sub box
	if hasSup {
		sup = layoutList(n.sup, ss)
	}
	if hasSub {
		sub = layoutList(n.sub, ss)
	}
	limits := false
	if op, ok := n.base.(bigop); ok {
		limits = n.limits > 0 || n.limits == 0 && s.display && op.limits
	}
	if limits {
		w := max(base.w, max(sup.w, sub.w))
		b := box{w: w, h: base.h, d: base.d}
		b.put(&base, (w-base.w)/2, 0)
		if hasSup {
			y := base.h + 0.15*s.size + sup.d
			b.put(&sup, (w-sup.w)/2, y)
			b.h = max(b.h, y+sup.h)
		}
		if hasSub {
			y := -base.d - 0.15*s.size - sub.h
			b.put(&sub, (w-sub.w)/2, y)
			b.d = max(b.d, -y+sub.d)
		}
		return b
	}
	var supY, subY float32
	if hasSup {
		supY = max(0.42*s.size, base.h-0.3*s.size)
	}
	if hasSub {
		subY = -max(0.16*s.size, base.d+0.08*s.size)
	}
	if hasSup && hasSub {
		if need := subY + sub.h + 0.08*s.size + sup.d; supY < need {
			supY = need
		}
	}
	dx := base.w + 0.02*s.size
	b := box{w: dx + max(sup.w, sub.w), h: base.h, d: base.d}
	b.put(&base, 0, 0)
	if hasSup {
		b.put(&sup, dx, supY)
		b.h = max(b.h, supY+sup.h)
	}
	if hasSub {
		b.put(&sub, dx, subY)
		b.d = max(b.d, -subY+sub.d)
	}
	return b
}

// layoutDelim lays out \left...\right: the body, with delimiters grown to
// its height.
func layoutDelim(n delim, s style) box {
	return wrapDelims(layoutList(n.body, s), n.left, n.right, s)
}

// wrapDelims puts delimiters around a laid-out body, sized to it.
func wrapDelims(body box, left, right string, s style) box {
	ext := body.h + body.d
	ds := s.size
	if ext > 0.8*s.size {
		ds = ext / 0.8
	}
	cy := (body.h - body.d) / 2
	gap := 0.06 * s.size
	b := box{h: body.h, d: body.d}
	x := float32(0)
	if left != "" {
		lb := sizedDelim(left, ds, cy)
		b.put(&lb, 0, 0)
		x = lb.w + gap
		b.h = max(b.h, lb.h)
		b.d = max(b.d, lb.d)
	}
	b.put(&body, x, 0)
	x += body.w
	if right != "" {
		rb := sizedDelim(right, ds, cy)
		x += gap
		b.put(&rb, x, 0)
		x += rb.w
		b.h = max(b.h, rb.h)
		b.d = max(b.d, rb.d)
	}
	b.w = x
	return b
}

// sizedDelim is one delimiter at a font size, its ink centered on cy above
// the baseline.
func sizedDelim(ch string, ds, cy float32) box {
	st := style{size: ds, upright: true}
	g, w := shape(ch, st, false)
	y := cy - 0.25*ds
	var b box
	b.w = w
	b.h = max(y+0.76*ds, 0)
	b.d = max(0.24*ds-y, 0)
	b.glyph(g, 0, y)
	return b
}

func layoutAccent(n accent, s style) box {
	body := layoutList(n.body, s)
	th := ruleThickness(s.size)
	gap := 0.08 * s.size
	wide := n.kind == accentWideHat || n.kind == accentWideTilde
	aw := body.w
	if n.kind != accentOverline && n.kind != accentUnderline {
		aw = max(body.w*0.95, 0.36*s.size)
		if wide {
			aw = max(body.w, 0.5*s.size)
		}
	}
	outW := max(body.w, aw)
	b := box{w: outW, h: body.h, d: body.d}
	b.put(&body, (outW-body.w)/2, 0)
	x := outW/2 - aw/2
	switch n.kind {
	case accentHat, accentWideHat:
		y := body.h + gap
		b.stroke(x, y, th*1.4, [][2]float32{{0, 0}, {aw / 2, 0.13 * s.size}, {aw, 0}})
		b.h = max(b.h, y+0.13*s.size+th)
	case accentBar, accentOverline:
		y := body.h + gap
		b.rect(x, y, aw, th)
		b.h = max(b.h, y+th)
	case accentUnderline:
		y := -(body.d + gap + th)
		b.rect(x, y, aw, th)
		b.d = max(b.d, -y)
	case accentVec, accentOver:
		y := body.h + gap + 0.02*s.size
		head := min(0.12*s.size, aw/2)
		b.stroke(x, y, th*1.2, [][2]float32{{0, 0}, {aw, 0}})
		b.stroke(x+aw-head, y, th*1.2, [][2]float32{{0, 0.05 * s.size}, {head, 0}})
		b.stroke(x+aw-head, y, th*1.2, [][2]float32{{0, -0.05 * s.size}, {head, 0}})
		b.h = max(b.h, y+0.05*s.size+th)
	case accentTilde, accentWideTilde:
		y := body.h + gap
		b.stroke(x, y, th*1.3, [][2]float32{
			{0, 0}, {0.25 * aw, 0.07 * s.size}, {0.5 * aw, 0.03 * s.size},
			{0.75 * aw, -0.01 * s.size}, {aw, 0.06 * s.size},
		})
		b.h = max(b.h, y+0.07*s.size+th)
	case accentDot, accentDdot:
		d := max(th*2, 0.06*s.size)
		y := body.h + gap
		if n.kind == accentDdot {
			b.rect(x+aw/2-d*1.6, y, d, d)
			b.rect(x+aw/2+d*0.6, y, d, d)
		} else {
			b.rect(x+aw/2-d/2, y, d, d)
		}
		b.h = max(b.h, y+d)
	}
	return b
}

// alignMode is how a table column places its cells.
type alignMode uint8

const (
	alignLeft alignMode = iota
	alignCenter
	alignRight
)

func layoutEnv(n envNode, s style) box {
	switch n.kind {
	case envCases:
		rows := tableBox(n.rows, []alignMode{alignLeft, alignLeft}, 0.9, s)
		return wrapDelims(rows, "{", "", s)
	case envMatrix:
		return tableBox(n.rows, nil, 0.6, s)
	case envPmatrix:
		return wrapDelims(tableBox(n.rows, nil, 0.6, s), "(", ")", s)
	case envBmatrix:
		return wrapDelims(tableBox(n.rows, nil, 0.6, s), "[", "]", s)
	case envVmatrix:
		return wrapDelims(tableBox(n.rows, nil, 0.6, s), "|", "|", s)
	case envAligned:
		// Columns alternate: each pair's first is flushed right against the
		// alignment point, the second flush left from it.
		ncol := 0
		for _, r := range n.rows {
			ncol = max(ncol, len(r))
		}
		aligns := make([]alignMode, ncol)
		for j := range aligns {
			if j%2 == 0 {
				aligns[j] = alignRight
			} else {
				aligns[j] = alignLeft
			}
		}
		return tableBox(n.rows, aligns, 0.5, s)
	case envArray:
		return tableBox(n.rows, specAligns(n.spec), 0.5, s)
	}
	return tableBox(n.rows, nil, 0.6, s)
}

// specAligns reads the column specification of an array, as in {lcr}, with
// its rules ({|c|}) ignored.
func specAligns(spec string) []alignMode {
	var aligns []alignMode
	for _, r := range spec {
		switch r {
		case 'l':
			aligns = append(aligns, alignLeft)
		case 'r':
			aligns = append(aligns, alignRight)
		case 'c':
			aligns = append(aligns, alignCenter)
		}
	}
	return aligns
}

// tableBox lays out rows of cells in columns, each column gap ems from the
// next, and stacks the rows.
func tableBox(rows [][]group, aligns []alignMode, gap float32, s style) box {
	cells := make([][]box, len(rows))
	ncol := 0
	for i, row := range rows {
		cells[i] = make([]box, len(row))
		for j, g := range row {
			cells[i][j] = layoutList(g, s)
		}
		ncol = max(ncol, len(row))
	}
	colw := make([]float32, ncol)
	for i := range cells {
		for j := range cells[i] {
			colw[j] = max(colw[j], cells[i][j].w)
		}
	}
	rowGap := gap * s.size
	rowBoxes := make([]box, len(rows))
	for i := range cells {
		var rb box
		x := float32(0)
		for j := range cells[i] {
			cb := &cells[i][j]
			var dx float32
			switch alignAt(aligns, j) {
			case alignCenter:
				dx = x + (colw[j]-cb.w)/2
			case alignRight:
				dx = x + colw[j] - cb.w
			default:
				dx = x
			}
			rb.put(cb, dx, 0)
			rb.h = max(rb.h, cb.h)
			rb.d = max(rb.d, cb.d)
			x += colw[j] + rowGap
		}
		if len(cells[i]) > 0 {
			rb.w = x - rowGap
		}
		rowBoxes[i] = rb
	}
	return stackRows(rowBoxes, 0.3*s.size, false)
}

func alignAt(aligns []alignMode, j int) alignMode {
	if j < len(aligns) {
		return aligns[j]
	}
	return alignCenter
}

// stackRows stacks laid-out rows with gap between them, and takes the first
// row's baseline as the whole's. Rows of a table keep their columns lined
// up, left at the same edge; lines of a formula are each centered.
func stackRows(rows []box, gap float32, center bool) box {
	if len(rows) == 0 {
		return box{}
	}
	w := float32(0)
	for i := range rows {
		w = max(w, rows[i].w)
	}
	out := box{w: w}
	y := float32(0)
	for i := range rows {
		r := &rows[i]
		if i > 0 {
			y -= rows[i-1].d + gap + r.h
		}
		dx := float32(0)
		if center {
			dx = (w - r.w) / 2
		}
		out.put(r, dx, y)
		out.h = max(out.h, y+r.h)
		out.d = max(out.d, -y+r.d)
	}
	return out
}

// layoutTop lays out a formula, stacking the lines that \\ breaks it into.
func layoutTop(g group, size float32, display bool) box {
	s := newStyle(size, display)
	var rows []box
	var cur group
	flush := func() {
		rows = append(rows, layoutList(cur, s))
		cur = nil
	}
	for _, n := range g {
		if _, ok := n.(lineBreak); ok {
			flush()
			continue
		}
		cur = append(cur, n)
	}
	if len(cur) > 0 || len(rows) == 0 {
		flush()
	}
	gap := 0.25 * size
	if display {
		gap = 0.55 * size
	}
	return stackRows(rows, gap, true)
}
