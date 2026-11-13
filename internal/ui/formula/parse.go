package formula

import (
	"errors"
	"fmt"
	"strings"
)

// node is a piece of parsed TeX; layout switches on its type.
type node any

// group is a list of nodes: the contents of braces, a script, a cell of an
// environment.
type group []node

// sym is a single character atom.
type sym struct {
	ch  string
	cls class
}

// runKind is how a text run is set.
type runKind uint8

const (
	runUpright runKind = iota // \text, \mathrm, \operatorname
	runBold                   // \mathbf, \mathbb
	runItalic                 // \mathit, \mathcal
)

// textRun is text whose characters are set as they are: \text{...}.
type textRun struct {
	text string
	kind runKind
	// op is an operator name (\operatorname), which leaves a thin space
	// around it.
	op bool
}

// styled is a group laid out with another face: \mathrm{...}, \mathbf{...}.
type styled struct {
	body group
	kind runKind
}

// fracKind is how a fraction sets its children.
type fracKind uint8

const (
	fracAuto fracKind = iota
	fracDisplay
	fracText
)

type frac struct {
	num, den group
	kind     fracKind
}

type sqrt struct {
	index group
	body  group
}

// script is a base with a superscript, a subscript, or both, and the
// \limits or \nolimits written for them.
type script struct {
	base   node
	sup    group
	sub    group
	limits int8 // 1 for \limits, -1 for \nolimits, 0 for the default
}

// bigop is a big operator such as \sum or \int.
type bigop struct {
	ch     string
	limits bool // limits above and below in display style
}

// delim is \left...\right.
type delim struct {
	left, right string
	body        group
}

// accentKind is an accent over or under its body.
type accentKind uint8

const (
	accentHat accentKind = iota
	accentWideHat
	accentBar
	accentOverline
	accentUnderline
	accentVec
	accentOver
	accentTilde
	accentWideTilde
	accentDot
	accentDdot
)

type accent struct {
	kind accentKind
	body group
}

// spacing is explicit space: \, \: \; \! \quad \qquad.
type spacing struct {
	em float32
}

// styleSwitch is \displaystyle or \textstyle, which set the style of the
// nodes after it.
type styleSwitch struct {
	display int8 // 1 display, -1 text
}

// envKind is a layout environment.
type envKind uint8

const (
	envCases envKind = iota
	envMatrix
	envPmatrix
	envBmatrix
	envVmatrix
	envAligned
	envArray
)

// envNode is \begin{...}...\end{...}: rows of cells, split at & and \\.
// spec is the column specification of an array.
type envNode struct {
	kind envKind
	rows [][]group
	spec string
}

// lineBreak is \\ outside an environment.
type lineBreak struct{}

// envKinds are the environments formula lays out.
var envKinds = map[string]envKind{
	"cases":   envCases,
	"matrix":  envMatrix,
	"pmatrix": envPmatrix,
	"bmatrix": envBmatrix,
	"vmatrix": envVmatrix,
	"aligned": envAligned,
	"align":   envAligned,
	"array":   envArray,
}

// maxDepth bounds the nesting the parser and layout accept, so that no
// input can exhaust the stack.
const maxDepth = 64

var errSyntax = errors.New("formula: malformed TeX")

// parseTex reads TeX into the nodes of one formula.
func parseTex(src string) (group, error) {
	p := &parser{toks: lex(src)}
	g, err := p.list(stopGroup)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		return nil, errSyntax
	}
	return g, nil
}

// stopMode is where a list of nodes ends.
type stopMode uint8

const (
	stopGroup   stopMode = iota // }, \right or \end
	stopRow                     // the above, & and \\
	stopBracket                 // ]
)

type parser struct {
	toks  []token
	pos   int
	depth int
}

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

func (p *parser) next() (token, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

func (p *parser) accept(k tokKind) bool {
	if t, ok := p.peek(); ok && t.kind == k {
		p.pos++
		return true
	}
	return false
}

func (p *parser) peekCmd(name string) bool {
	t, ok := p.peek()
	return ok && t.kind == tokCmd && t.text == name
}

// list reads nodes until the stop its mode names or the end of input.
func (p *parser) list(mode stopMode) (group, error) {
	p.depth++
	if p.depth > maxDepth {
		return nil, errSyntax
	}
	defer func() { p.depth-- }()
	g := group{}
	for {
		t, ok := p.peek()
		if !ok {
			return g, nil
		}
		switch t.kind {
		case tokSpace:
			p.pos++
			continue
		case tokNBSP:
			p.pos++
			g = append(g, spacing{em: 1.0 / 3})
			continue
		case tokRBrace:
			if mode == stopGroup || mode == stopRow {
				return g, nil
			}
			return nil, errSyntax
		case tokAmp:
			if mode == stopRow {
				return g, nil
			}
			return nil, errSyntax
		case tokNewline:
			if mode == stopRow {
				return g, nil
			}
			p.pos++
			p.skipBreakSpace()
			g = append(g, lineBreak{})
			continue
		case tokRBracket:
			if mode == stopBracket {
				return g, nil
			}
			p.pos++
			g = append(g, sym{ch: "]", cls: classClose})
			continue
		case tokCmd:
			if t.text == "right" || t.text == "end" {
				if mode == stopBracket {
					return nil, errSyntax
				}
				return g, nil
			}
		}
		n, err := p.atom()
		if err != nil {
			return nil, err
		}
		g = append(g, n)
	}
}

// atom reads one atom with the scripts and limits that follow it.
func (p *parser) atom() (node, error) {
	p.depth++
	if p.depth > maxDepth {
		return nil, errSyntax
	}
	defer func() { p.depth-- }()
	t, ok := p.next()
	if !ok {
		return nil, errSyntax
	}
	var base node
	switch t.kind {
	case tokChar:
		base = sym{ch: t.text, cls: charClass([]rune(t.text)[0])}
	case tokLBracket:
		base = sym{ch: "[", cls: classOpen}
	case tokRBrace:
		return nil, errSyntax
	case tokLBrace:
		g, err := p.list(stopGroup)
		if err != nil {
			return nil, err
		}
		if !p.accept(tokRBrace) {
			return nil, errSyntax
		}
		base = g
	case tokCmd:
		n, err := p.command(t.text)
		if err != nil {
			return nil, err
		}
		base = n
	default:
		return nil, errSyntax
	}
	return p.scripts(base)
}

// scripts attaches the superscripts, subscripts and limits that follow a
// base.
func (p *parser) scripts(base node) (node, error) {
	var sup, sub group
	limits := int8(0)
	for {
		t, ok := p.peek()
		if !ok {
			break
		}
		if t.kind == tokSpace {
			p.pos++
			continue
		}
		if t.kind == tokCmd && (t.text == "limits" || t.text == "nolimits") {
			if t.text == "limits" {
				limits = 1
			} else {
				limits = -1
			}
			p.pos++
			continue
		}
		if t.kind != tokSup && t.kind != tokSub {
			break
		}
		p.pos++
		arg, err := p.arg()
		if err != nil {
			return nil, err
		}
		if t.kind == tokSup {
			if sup != nil {
				return nil, errSyntax
			}
			sup = arg
		} else {
			if sub != nil {
				return nil, errSyntax
			}
			sub = arg
		}
	}
	if sup == nil && sub == nil && limits == 0 {
		return base, nil
	}
	return script{base: base, sup: sup, sub: sub, limits: limits}, nil
}

// arg reads the argument of a command: a braced group, or else one atom.
func (p *parser) arg() (group, error) {
	if t, ok := p.peek(); ok && t.kind == tokLBrace {
		p.pos++
		g, err := p.list(stopGroup)
		if err != nil {
			return nil, err
		}
		if !p.accept(tokRBrace) {
			return nil, errSyntax
		}
		return g, nil
	}
	n, err := p.atom()
	if err != nil {
		return nil, err
	}
	return group{n}, nil
}

// command reads what a control sequence builds.
func (p *parser) command(name string) (node, error) {
	switch name {
	case "frac", "dfrac", "tfrac":
		num, err := p.arg()
		if err != nil {
			return nil, err
		}
		den, err := p.arg()
		if err != nil {
			return nil, err
		}
		kind := fracAuto
		switch name {
		case "dfrac":
			kind = fracDisplay
		case "tfrac":
			kind = fracText
		}
		return frac{num: num, den: den, kind: kind}, nil
	case "sqrt":
		var index group
		if t, ok := p.peek(); ok && t.kind == tokLBracket {
			p.pos++
			g, err := p.list(stopBracket)
			if err != nil {
				return nil, err
			}
			if !p.accept(tokRBracket) {
				return nil, errSyntax
			}
			index = g
		}
		body, err := p.arg()
		if err != nil {
			return nil, err
		}
		return sqrt{index: index, body: body}, nil
	case "left":
		left, err := p.delim()
		if err != nil {
			return nil, err
		}
		body, err := p.list(stopGroup)
		if err != nil {
			return nil, err
		}
		if !p.peekCmd("right") {
			return nil, errSyntax
		}
		p.pos++
		right, err := p.delim()
		if err != nil {
			return nil, err
		}
		return delim{left: left, right: right, body: body}, nil
	case "right", "end", "limits", "nolimits":
		return nil, errSyntax
	case "begin":
		return p.environment()
	case "text", "mbox":
		s, err := p.textArg()
		if err != nil {
			return nil, err
		}
		return textRun{text: s, kind: runUpright}, nil
	case "operatorname":
		s, err := p.textArg()
		if err != nil {
			return nil, err
		}
		return textRun{text: s, kind: runUpright, op: true}, nil
	case "mathrm", "mathsf", "mathtt":
		g, err := p.arg()
		if err != nil {
			return nil, err
		}
		return styled{body: g, kind: runUpright}, nil
	case "mathbf":
		g, err := p.arg()
		if err != nil {
			return nil, err
		}
		return styled{body: g, kind: runBold}, nil
	case "mathbb":
		// No blackboard font is promised: bold is the best effort.
		g, err := p.arg()
		if err != nil {
			return nil, err
		}
		return styled{body: g, kind: runBold}, nil
	case "mathit":
		g, err := p.arg()
		if err != nil {
			return nil, err
		}
		return styled{body: g, kind: runItalic}, nil
	case "mathcal":
		// No calligraphic font is promised: italics stand in for it.
		g, err := p.arg()
		if err != nil {
			return nil, err
		}
		return styled{body: g, kind: runItalic}, nil
	case "hat":
		return p.accent(accentHat)
	case "widehat":
		return p.accent(accentWideHat)
	case "bar":
		return p.accent(accentBar)
	case "overline":
		return p.accent(accentOverline)
	case "underline":
		return p.accent(accentUnderline)
	case "vec":
		return p.accent(accentVec)
	case "overrightarrow":
		return p.accent(accentOver)
	case "tilde":
		return p.accent(accentTilde)
	case "widetilde":
		return p.accent(accentWideTilde)
	case "dot":
		return p.accent(accentDot)
	case "ddot":
		return p.accent(accentDdot)
	case "not":
		n, err := p.atom()
		if err != nil {
			return nil, err
		}
		if s, ok := n.(sym); ok {
			if m, ok := notSymbols[s.ch]; ok {
				return sym{ch: m, cls: classRel}, nil
			}
		}
		return nil, errSyntax
	case "displaystyle":
		return styleSwitch{display: 1}, nil
	case "textstyle":
		return styleSwitch{display: -1}, nil
	}
	if s, ok := symbols[name]; ok {
		if s.big {
			return bigop{ch: s.ch, limits: s.limits}, nil
		}
		return sym{ch: s.ch, cls: s.cls}, nil
	}
	switch name {
	case ",", "thinspace":
		return spacing{em: 3.0 / 18}, nil
	case ":":
		return spacing{em: 4.0 / 18}, nil
	case ";", "thickspace":
		return spacing{em: 5.0 / 18}, nil
	case "!":
		return spacing{em: -3.0 / 18}, nil
	case " ":
		return spacing{em: 1.0 / 3}, nil
	case "enspace":
		return spacing{em: 0.5}, nil
	case "quad":
		return spacing{em: 1}, nil
	case "qquad":
		return spacing{em: 2}, nil
	}
	return nil, fmt.Errorf("formula: unsupported command \\%s", name)
}

func (p *parser) accent(kind accentKind) (node, error) {
	body, err := p.arg()
	if err != nil {
		return nil, err
	}
	return accent{kind: kind, body: body}, nil
}

// textArg reads the raw text of \text{...}, spaces included, with nested
// braces kept.
func (p *parser) textArg() (string, error) {
	if !p.accept(tokLBrace) {
		return "", errSyntax
	}
	var sb strings.Builder
	depth := 1
	for {
		t, ok := p.next()
		if !ok {
			return "", errSyntax
		}
		switch t.kind {
		case tokLBrace:
			depth++
			sb.WriteByte('{')
		case tokRBrace:
			depth--
			if depth == 0 {
				return sb.String(), nil
			}
			sb.WriteByte('}')
		case tokChar:
			sb.WriteString(t.text)
		case tokSpace, tokNBSP:
			sb.WriteByte(' ')
		case tokSup:
			sb.WriteByte('^')
		case tokSub:
			sb.WriteByte('_')
		case tokCmd:
			switch t.text {
			case " ", ",":
				sb.WriteByte(' ')
			case "{", "}", "%", "&", "_", "#", "$", "|":
				sb.WriteString(t.text)
			}
		}
	}
}

// environment reads \begin{name} to \end{name}, after \begin.
func (p *parser) environment() (node, error) {
	name, err := p.envName()
	if err != nil {
		return nil, err
	}
	kind, ok := envKinds[name]
	if !ok {
		return nil, fmt.Errorf("formula: unsupported environment %s", name)
	}
	spec := ""
	if kind == envArray {
		// The column specification, as in {lcr}.
		if spec, err = p.textArg(); err != nil {
			return nil, err
		}
	}
	var rows [][]group
	row := []group{}
	for {
		cell, err := p.list(stopRow)
		if err != nil {
			return nil, err
		}
		row = append(row, cell)
		t, ok := p.peek()
		if !ok {
			return nil, errSyntax
		}
		switch {
		case t.kind == tokAmp:
			p.pos++
		case t.kind == tokNewline:
			p.pos++
			p.skipBreakSpace()
			rows = append(rows, row)
			row = []group{}
		case t.kind == tokCmd && t.text == "end":
			p.pos++
			end, err := p.envName()
			if err != nil {
				return nil, err
			}
			if end != name {
				return nil, errSyntax
			}
			rows = append(rows, row)
			return envNode{kind: kind, rows: trimEmptyRows(rows), spec: spec}, nil
		default:
			return nil, errSyntax
		}
	}
}

// trimEmptyRows drops the empty row a trailing \\ leaves.
func trimEmptyRows(rows [][]group) [][]group {
	for len(rows) > 0 {
		last := rows[len(rows)-1]
		if len(last) == 1 && len(last[0]) == 0 {
			rows = rows[:len(rows)-1]
			continue
		}
		break
	}
	return rows
}

func (p *parser) envName() (string, error) {
	if !p.accept(tokLBrace) {
		return "", errSyntax
	}
	var sb strings.Builder
	for {
		t, ok := p.next()
		if !ok {
			return "", errSyntax
		}
		if t.kind == tokRBrace {
			return sb.String(), nil
		}
		sb.WriteString(t.text)
	}
}

// skipBreakSpace drops the optional space after \\, as in \\[2pt].
func (p *parser) skipBreakSpace() {
	if t, ok := p.peek(); ok && t.kind == tokLBracket {
		for {
			t, ok := p.next()
			if !ok || t.kind == tokRBracket {
				return
			}
		}
	}
}

// delim reads the delimiter of \left or \right, "" for a period, which
// leaves that side empty.
func (p *parser) delim() (string, error) {
	t, ok := p.next()
	if !ok {
		return "", errSyntax
	}
	switch t.kind {
	case tokChar:
		if t.text == "." {
			return "", nil
		}
		if strings.Contains(delimiterChars, t.text) {
			return t.text, nil
		}
		return "", errSyntax
	case tokLBracket:
		return "[", nil
	case tokRBracket:
		return "]", nil
	case tokCmd:
		if s, ok := symbols[t.text]; ok && strings.Contains("⟨⟩⌊⌋⌈⌉{[]}‖|∖↑↓", s.ch) {
			return s.ch, nil
		}
	}
	return "", errSyntax
}
