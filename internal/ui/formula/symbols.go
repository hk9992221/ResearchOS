package formula

import (
	"strings"
	"unicode"
)

// class is the TeX atom class of a node, which decides the space around it.
type class uint8

const (
	classOrd class = iota
	classOp
	classBin
	classRel
	classOpen
	classClose
	classPunct
	classInner
)

// symInfo is what a control sequence stands for: the character it shows and
// how it spaces.
type symInfo struct {
	ch  string
	cls class
	// big marks a big operator, which display style draws larger.
	big bool
	// limits is whether its scripts go above and below in display style.
	limits bool
}

// symbols maps control sequence names, without the backslash, to their
// characters. It covers what research notes use: Greek, relations,
// operators, arrows, big operators and delimiters.
var symbols = map[string]symInfo{
	// Greek letters.
	"alpha":      {"α", classOrd, false, false},
	"beta":       {"β", classOrd, false, false},
	"gamma":      {"γ", classOrd, false, false},
	"delta":      {"δ", classOrd, false, false},
	"epsilon":    {"ϵ", classOrd, false, false},
	"varepsilon": {"ε", classOrd, false, false},
	"zeta":       {"ζ", classOrd, false, false},
	"eta":        {"η", classOrd, false, false},
	"theta":      {"θ", classOrd, false, false},
	"vartheta":   {"ϑ", classOrd, false, false},
	"iota":       {"ι", classOrd, false, false},
	"kappa":      {"κ", classOrd, false, false},
	"lambda":     {"λ", classOrd, false, false},
	"mu":         {"μ", classOrd, false, false},
	"nu":         {"ν", classOrd, false, false},
	"xi":         {"ξ", classOrd, false, false},
	"omicron":    {"ο", classOrd, false, false},
	"pi":         {"π", classOrd, false, false},
	"varpi":      {"ϖ", classOrd, false, false},
	"rho":        {"ρ", classOrd, false, false},
	"varrho":     {"ϱ", classOrd, false, false},
	"sigma":      {"σ", classOrd, false, false},
	"varsigma":   {"ς", classOrd, false, false},
	"tau":        {"τ", classOrd, false, false},
	"upsilon":    {"υ", classOrd, false, false},
	"phi":        {"ϕ", classOrd, false, false},
	"varphi":     {"φ", classOrd, false, false},
	"chi":        {"χ", classOrd, false, false},
	"psi":        {"ψ", classOrd, false, false},
	"omega":      {"ω", classOrd, false, false},

	"Alpha":   {"Α", classOrd, false, false},
	"Beta":    {"Β", classOrd, false, false},
	"Gamma":   {"Γ", classOrd, false, false},
	"Delta":   {"Δ", classOrd, false, false},
	"Epsilon": {"Ε", classOrd, false, false},
	"Zeta":    {"Ζ", classOrd, false, false},
	"Eta":     {"Η", classOrd, false, false},
	"Theta":   {"Θ", classOrd, false, false},
	"Iota":    {"Ι", classOrd, false, false},
	"Kappa":   {"Κ", classOrd, false, false},
	"Lambda":  {"Λ", classOrd, false, false},
	"Mu":      {"Μ", classOrd, false, false},
	"Nu":      {"Ν", classOrd, false, false},
	"Xi":      {"Ξ", classOrd, false, false},
	"Omicron": {"Ο", classOrd, false, false},
	"Pi":      {"Π", classOrd, false, false},
	"Rho":     {"Ρ", classOrd, false, false},
	"Sigma":   {"Σ", classOrd, false, false},
	"Tau":     {"Τ", classOrd, false, false},
	"Upsilon": {"Υ", classOrd, false, false},
	"Phi":     {"Φ", classOrd, false, false},
	"Chi":     {"Χ", classOrd, false, false},
	"Psi":     {"Ψ", classOrd, false, false},
	"Omega":   {"Ω", classOrd, false, false},

	// Ordinary symbols.
	"infty":         {"∞", classOrd, false, false},
	"partial":       {"∂", classOrd, false, false},
	"nabla":         {"∇", classOrd, false, false},
	"ell":           {"ℓ", classOrd, false, false},
	"hbar":          {"ℏ", classOrd, false, false},
	"imath":         {"ı", classOrd, false, false},
	"jmath":         {"ȷ", classOrd, false, false},
	"emptyset":      {"∅", classOrd, false, false},
	"varnothing":    {"∅", classOrd, false, false},
	"forall":        {"∀", classOrd, false, false},
	"exists":        {"∃", classOrd, false, false},
	"nexists":       {"∄", classOrd, false, false},
	"neg":           {"¬", classOrd, false, false},
	"lnot":          {"¬", classOrd, false, false},
	"angle":         {"∠", classOrd, false, false},
	"measuredangle": {"∡", classOrd, false, false},
	"top":           {"⊤", classOrd, false, false},
	"bot":           {"⊥", classOrd, false, false},
	"Re":            {"ℜ", classOrd, false, false},
	"Im":            {"ℑ", classOrd, false, false},
	"aleph":         {"ℵ", classOrd, false, false},
	"prime":         {"′", classOrd, false, false},
	"degree":        {"°", classOrd, false, false},
	"dots":          {"…", classOrd, false, false},
	"ldots":         {"…", classOrd, false, false},
	"cdots":         {"⋯", classOrd, false, false},
	"vdots":         {"⋮", classOrd, false, false},
	"ddots":         {"⋱", classOrd, false, false},
	"therefore":     {"∴", classOrd, false, false},
	"because":       {"∵", classOrd, false, false},
	"checkmark":     {"✓", classOrd, false, false},
	"dagger":        {"†", classOrd, false, false},
	"ddagger":       {"‡", classOrd, false, false},

	// Relations.
	"leq":       {"≤", classRel, false, false},
	"le":        {"≤", classRel, false, false},
	"geq":       {"≥", classRel, false, false},
	"ge":        {"≥", classRel, false, false},
	"neq":       {"≠", classRel, false, false},
	"ne":        {"≠", classRel, false, false},
	"approx":    {"≈", classRel, false, false},
	"equiv":     {"≡", classRel, false, false},
	"sim":       {"∼", classRel, false, false},
	"simeq":     {"≃", classRel, false, false},
	"cong":      {"≅", classRel, false, false},
	"propto":    {"∝", classRel, false, false},
	"ll":        {"≪", classRel, false, false},
	"gg":        {"≫", classRel, false, false},
	"prec":      {"≺", classRel, false, false},
	"succ":      {"≻", classRel, false, false},
	"preceq":    {"⪯", classRel, false, false},
	"succeq":    {"⪰", classRel, false, false},
	"subset":    {"⊂", classRel, false, false},
	"subseteq":  {"⊆", classRel, false, false},
	"subsetneq": {"⊊", classRel, false, false},
	"supset":    {"⊃", classRel, false, false},
	"supseteq":  {"⊇", classRel, false, false},
	"supsetneq": {"⊋", classRel, false, false},
	"in":        {"∈", classRel, false, false},
	"notin":     {"∉", classRel, false, false},
	"ni":        {"∋", classRel, false, false},
	"owns":      {"∋", classRel, false, false},
	"perp":      {"⊥", classRel, false, false},
	"parallel":  {"∥", classRel, false, false},
	"nparallel": {"∦", classRel, false, false},
	"mid":       {"∣", classRel, false, false},
	"nmid":      {"∤", classRel, false, false},
	"doteq":     {"≐", classRel, false, false},
	"models":    {"⊨", classRel, false, false},
	"vdash":     {"⊢", classRel, false, false},
	"dashv":     {"⊣", classRel, false, false},
	"asymp":     {"≍", classRel, false, false},
	"bowtie":    {"⋈", classRel, false, false},
	"smile":     {"⌣", classRel, false, false},
	"frown":     {"⌢", classRel, false, false},

	// Binary operators.
	"pm":            {"±", classBin, false, false},
	"mp":            {"∓", classBin, false, false},
	"times":         {"×", classBin, false, false},
	"cdot":          {"⋅", classBin, false, false},
	"div":           {"÷", classBin, false, false},
	"ast":           {"∗", classBin, false, false},
	"star":          {"⋆", classBin, false, false},
	"circ":          {"∘", classBin, false, false},
	"bullet":        {"•", classBin, false, false},
	"oplus":         {"⊕", classBin, false, false},
	"ominus":        {"⊖", classBin, false, false},
	"otimes":        {"⊗", classBin, false, false},
	"odot":          {"⊙", classBin, false, false},
	"cup":           {"∪", classBin, false, false},
	"cap":           {"∩", classBin, false, false},
	"setminus":      {"∖", classBin, false, false},
	"smallsetminus": {"∖", classBin, false, false},
	"uplus":         {"⊎", classBin, false, false},
	"sqcup":         {"⊔", classBin, false, false},
	"sqcap":         {"⊓", classBin, false, false},
	"wedge":         {"∧", classBin, false, false},
	"land":          {"∧", classBin, false, false},
	"vee":           {"∨", classBin, false, false},
	"lor":           {"∨", classBin, false, false},
	"boxplus":       {"⊞", classBin, false, false},
	"boxminus":      {"⊟", classBin, false, false},
	"triangleleft":  {"◁", classBin, false, false},
	"triangleright": {"▷", classBin, false, false},

	// Arrows.
	"rightarrow":         {"→", classRel, false, false},
	"to":                 {"→", classRel, false, false},
	"leftarrow":          {"←", classRel, false, false},
	"gets":               {"←", classRel, false, false},
	"leftrightarrow":     {"↔", classRel, false, false},
	"Rightarrow":         {"⇒", classRel, false, false},
	"implies":            {"⟹", classRel, false, false},
	"Leftarrow":          {"⇐", classRel, false, false},
	"Leftrightarrow":     {"⇔", classRel, false, false},
	"iff":                {"⟺", classRel, false, false},
	"mapsto":             {"↦", classRel, false, false},
	"longrightarrow":     {"⟶", classRel, false, false},
	"longleftarrow":      {"⟵", classRel, false, false},
	"Longrightarrow":     {"⟹", classRel, false, false},
	"Longleftarrow":      {"⟸", classRel, false, false},
	"longleftrightarrow": {"⟷", classRel, false, false},
	"Longleftrightarrow": {"⟺", classRel, false, false},
	"uparrow":            {"↑", classRel, false, false},
	"downarrow":          {"↓", classRel, false, false},
	"updownarrow":        {"↕", classRel, false, false},
	"nearrow":            {"↗", classRel, false, false},
	"searrow":            {"↘", classRel, false, false},
	"swarrow":            {"↙", classRel, false, false},
	"nwarrow":            {"↖", classRel, false, false},
	"hookrightarrow":     {"↪", classRel, false, false},
	"rightharpoonup":     {"⇀", classRel, false, false},
	"rightleftharpoons":  {"⇌", classRel, false, false},

	// Big operators: limits above and below in display style, but integrals
	// keep theirs to the side, as TeX does.
	"sum":       {"∑", classOp, true, true},
	"prod":      {"∏", classOp, true, true},
	"coprod":    {"∐", classOp, true, true},
	"bigcup":    {"⋃", classOp, true, true},
	"bigcap":    {"⋂", classOp, true, true},
	"bigoplus":  {"⨁", classOp, true, true},
	"bigotimes": {"⨂", classOp, true, true},
	"bigodot":   {"⨀", classOp, true, true},
	"bigvee":    {"⋁", classOp, true, true},
	"bigwedge":  {"⋀", classOp, true, true},
	"bigsqcup":  {"⨆", classOp, true, true},
	"biguplus":  {"⨄", classOp, true, true},
	"int":       {"∫", classOp, true, false},
	"iint":      {"∬", classOp, true, false},
	"iiint":     {"∭", classOp, true, false},
	"oint":      {"∮", classOp, true, false},

	// Delimiters, for \left and \right and as atoms of their own.
	"langle":    {"⟨", classOpen, false, false},
	"rangle":    {"⟩", classClose, false, false},
	"lfloor":    {"⌊", classOpen, false, false},
	"rfloor":    {"⌋", classClose, false, false},
	"lceil":     {"⌈", classOpen, false, false},
	"rceil":     {"⌉", classClose, false, false},
	"lbrace":    {"{", classOpen, false, false},
	"rbrace":    {"}", classClose, false, false},
	"lbrack":    {"[", classOpen, false, false},
	"rbrack":    {"]", classClose, false, false},
	"vert":      {"|", classOrd, false, false},
	"lvert":     {"|", classOpen, false, false},
	"rvert":     {"|", classClose, false, false},
	"Vert":      {"‖", classOrd, false, false},
	"lVert":     {"‖", classOpen, false, false},
	"rVert":     {"‖", classClose, false, false},
	"backslash": {"∖", classOrd, false, false},
	"|":         {"‖", classOrd, false, false},
}

// delimiterChars are the characters \left and \right take as delimiters.
const delimiterChars = "()[]{}|./<>"

// charClass returns the class of a character typed as itself.
func charClass(r rune) class {
	switch {
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return classOrd
	case strings.ContainsRune("+-*/±∓×÷⋅∗⋆∘•⊕⊗∪∩∧∨", r):
		return classBin
	case strings.ContainsRune("=<>≤≥≠≈≡∼≃∝⊂⊆⊃⊇∈∉∋∣∥⊥≪≫≐⊢⊨→←↔⇒⇐⇔↦", r):
		return classRel
	case strings.ContainsRune("([{⟨⌊⌈", r):
		return classOpen
	case strings.ContainsRune(")]}⟩⌋⌉", r):
		return classClose
	case strings.ContainsRune(",;.", r):
		return classPunct
	}
	return classOrd
}

// isVarRune reports whether a symbol's character is a variable, which math
// sets in italics: a Latin or lowercase Greek letter.
func isVarRune(ch string) bool {
	rs := []rune(ch)
	if len(rs) != 1 {
		return false
	}
	r := rs[0]
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= 'A' && r <= 'Z' {
		return true
	}
	return unicode.Is(unicode.Greek, r) && unicode.IsLower(r)
}

// notSymbols maps a character to what \not makes of it.
var notSymbols = map[string]string{
	"=": "≠",
	"∈": "∉",
	"∋": "∌",
	"⊂": "⊄",
	"⊆": "⊈",
	"⊃": "⊅",
	"⊇": "⊉",
	"≡": "≢",
	"∼": "≁",
	"≈": "≉",
	"∣": "∤",
	"∥": "∦",
	"≤": "≰",
	"≥": "≱",
}
