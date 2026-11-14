package formula

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

// BaKoMa Computer Modern TrueType fonts, the classic TeX face, from
// CTAN:fonts/cm/ps-type1/bakoma/ttf/. The licence (fonts/LICENCE) permits
// copying and distributing the fonts with the copyright notice, which the
// files and the licence text carry.
//
// The three faces register under one family so the text engine picks the
// style a run asks for: cmr10 upright, cmmi10 for italic variables — the
// math italic of TeX, not a slanted roman — and cmbx10 bold.
//
//go:embed fonts/cmr10.ttf
var cmRoman []byte

//go:embed fonts/cmmi10.ttf
var cmMathItalic []byte

//go:embed fonts/cmbx10.ttf
var cmBold []byte

// cmFamily is the family the embedded faces share, first in the Math stack.
const cmFamily = "CM Serif"

func init() {
	// A platform whose text engine cannot take in-memory fonts keeps the
	// installed stack; the renderer never depends on these bytes alone.
	for _, f := range [][]byte{cmRoman, cmMathItalic, cmBold} {
		_ = ui.RegisterFont(f, cmFamily)
	}
}
