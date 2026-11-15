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

// STIX Two, the OpenType Unicode math fonts of the STIX project, from
// CTAN:fonts/stix2-otf/, under the SIL Open Font License 1.1 (fonts/OFL.txt),
// which permits embedding and bundling them with software provided the
// licence and copyright notice travel with them, as they do here. The files
// are the unmodified originals, so they keep their own names.
//
// STIX Two Math carries the upright letters, the digits, the operators and
// the symbols, but only one face: italic variables and the semibold of
// \mathbf come from STIX Two Text, the same typeface, so that a formula
// never mixes two designs.
//
//go:embed fonts/STIXTwoMath-Regular.otf
var stixMath []byte

//go:embed fonts/STIXTwoText-Italic.otf
var stixTextItalic []byte

//go:embed fonts/STIXTwoText-SemiBold.otf
var stixTextSemiBold []byte

// The families the embedded faces answer to. CM Serif is the stand-in after
// STIX Two; the two STIX families are the two faces of one design, the math
// font for upright runs and the text font for the styles it lacks.
const (
	cmFamily       = "CM Serif"
	stixMathFamily = "STIX Two Math"
	stixTextFamily = "STIX Two Text"
)

func init() {
	// A platform whose text engine cannot take in-memory fonts keeps the
	// installed stack; the renderer never depends on these bytes alone.
	for _, f := range [][]byte{cmRoman, cmMathItalic, cmBold} {
		_ = ui.RegisterFont(f, cmFamily)
	}
	_ = ui.RegisterFont(stixMath, stixMathFamily)
	for _, f := range [][]byte{stixTextItalic, stixTextSemiBold} {
		_ = ui.RegisterFont(f, stixTextFamily)
	}
}
