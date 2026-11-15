package components

// Generation presence: the progress panel and the animated companion shown
// while a long AI generation runs (weekly report sections, and any operation
// that reports steps).
//
// The panel is deliberately one component: an overall bar that follows the
// streamed percentage, a row of section ticks, the phase the model is in and
// the elapsed time. The companion is drawn from primitives only — a
// round-bottom flask with eyes, liquid and rising bubbles — so it needs no
// assets, follows the appearance, and costs a few fills a frame.
//
// Both honour the desktop's reduced-motion setting: the bar snaps to its
// target, the flask holds a still pose (the phase still shows, in its eyes and
// liquid), and nothing moves. Callers therefore never need a second static
// variant.

import (
	"math"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// GenPhase is what a generation is doing right now.
type GenPhase int

const (
	// GenPhasePreparing is a generation that has not streamed anything yet,
	// as while the digest is collected.
	GenPhasePreparing GenPhase = iota
	// GenPhaseThinking is a call waiting for the model's first text.
	GenPhaseThinking
	// GenPhaseWriting is a call receiving streamed text.
	GenPhaseWriting
	// GenPhaseDone is a finished generation.
	GenPhaseDone
	// GenPhaseFailed is a generation that stopped with an error.
	GenPhaseFailed
)

// GenStepState is where one step of a generation stands.
type GenStepState int

const (
	// GenStepPending is a step not started.
	GenStepPending GenStepState = iota
	// GenStepActive is the step being written now.
	GenStepActive
	// GenStepDone is a finished step.
	GenStepDone
)

// GenStep is one tick of the panel's checklist: a section of the report.
type GenStep struct {
	Label string
	State GenStepState
}

// GenProgressOptions configures the progress panel.
type GenProgressOptions struct {
	// Title names the operation, as "重新生成周报". Empty omits it.
	Title string
	Phase GenPhase
	// Percent is the overall progress from 0 to 1; negative is an unknown
	// length, drawn as a subtly pulsing bar.
	Percent float64
	// Steps are the section ticks, in order. Empty omits the tick row.
	Steps []GenStep
	// Index and Total make the "3/7" counter; Total 0 hides it.
	Index, Total int
	// Current names the step in progress, shown after the counter.
	Current string
	// Elapsed is the time since the generation started; zero hides it.
	Elapsed time.Duration
	// CompanionSize draws the companion at that size in DIPs; zero omits
	// it, for a caller that places it itself.
	CompanionSize float32
	// Compact lays the panel out for the task strip: one row, no title
	// column, smaller ticks.
	Compact bool
}

// GenProgress builds the progress panel: percent bar, section ticks, phase and
// elapsed time, with the companion beside them at CompanionSize.
func (u *UI) GenProgress(o GenProgressOptions) ui.Element {
	if o.Compact {
		return u.genPanelRow(o, true)
	}
	p := u.T.Palette
	return ui.Column(u.C).FillWidth().Gap(tokens.Space3).Children(func() {
		if o.Title != "" {
			ui.Text(u.C, o.Title).
				FontSize(tokens.Typography.Secondary.Size).
				FontWeight(int(tokens.Typography.Secondary.Weight)).
				TextColor(mygo.Color(p.TextPrimary))
		}
		u.genPanelRow(o, false)
	})
}

// genPanelRow is the panel without its title: companion, then the column of
// phase, bar and ticks.
func (u *UI) genPanelRow(o GenProgressOptions, compact bool) ui.Element {
	p := u.T.Palette
	row := ui.Row(u.C).Gap(tokens.Space2).AlignItems(ui.Center)
	if !compact {
		row = ui.Row(u.C).FillWidth().Gap(tokens.Space3).AlignItems(ui.Center)
	}
	row.Children(func() {
		if o.CompanionSize > 0 {
			u.GenCompanion(o.Phase, o.CompanionSize)
		}
		col := ui.Column(u.C).Gap(tokens.Space1).AlignItems(ui.Start)
		if compact {
			// A row sized by its content would collapse the bar and the
			// ticks: the strip gives them a fixed width and a tighter
			// vertical rhythm than the dialog.
			col = col.Width(tokens.Space12 * 8).Gap(2)
		}
		col.Children(func() {
			ui.Row(u.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
				// Every label is one line: the strip is 40px high and a
				// wrapped word would push the bar out of it.
				if compact && o.Title != "" {
					ui.Text(u.C, o.Title).
						FontSize(tokens.Typography.Secondary.Size).
						SingleLine().
						TextColor(mygo.Color(p.TextPrimary))
				}
				ui.Text(u.C, genPhaseText(o.Phase)).
					FontSize(tokens.Typography.Caption.Size).
					SingleLine().
					TextColor(mygo.Color(genPhaseColor(p, o.Phase)))
				if o.Total > 0 {
					ui.Text(u.C, genCounter(o.Index, o.Total, o.Current)).
						FontSize(tokens.Typography.Caption.Size).
						SingleLine().
						TextColor(mygo.Color(p.TextMuted))
				}
				ui.Spacer(u.C)
				if o.Elapsed > 0 {
					ui.Text(u.C, genElapsed(o.Elapsed)).
						FontSize(tokens.Typography.Caption.Size).
						SingleLine().
						TextColor(mygo.Color(p.TextMuted))
				}
			})
			u.genBar(o.Percent, compact)
			if len(o.Steps) > 0 {
				u.genTicks(o.Steps, compact)
			}
		})
	})
	return row
}

// genBar draws the overall bar. The fill follows Percent with a spring, so a
// section transition slides instead of jumping; reduced motion snaps it.
func (u *UI) genBar(percent float64, compact bool) {
	height := float32(tokens.Space1) + 2
	if compact {
		height = 4
	}
	p := u.T.Palette
	bar := ui.Box(u.C).FillWidth().Height(height).Key("gen-bar")
	target := float32(percent)
	if percent < 0 {
		target = 0
	}
	target = max(0, min(1, target))
	value := bar.AnimateTo("fill", target, ui.Smooth)
	shimmer := float32(0)
	if percent < 0 {
		t := bar.Loop("indeterminate", 1400*time.Millisecond, ui.EaseInOut)
		shimmer = float32(math.Sin(float64(t) * math.Pi * 2))
	}
	accent := mygo.Color(p.AccentDefault)
	track := mygo.Color(p.BorderStrong)
	bar.Draw(func(paint *ui.Painter, r ui.Rect) {
		radius := r.H / 2
		paint.Fill(r, track, radius)
		if percent < 0 {
			// Unknown length: a short accent band crossing the track.
			w := r.W * 0.28
			x := r.X + (r.W-w)*(0.5+0.5*shimmer)
			paint.Fill(ui.Rect{X: x, Y: r.Y, W: w, H: r.H}, accent, radius)
			return
		}
		w := r.W * value
		if w <= 0 {
			return
		}
		paint.Fill(ui.Rect{X: r.X, Y: r.Y, W: w, H: r.H}, accent, radius)
	})
}

// genTicks draws one tick per step: a segment filled when the step is done,
// accent-tinted and pulsing while it is being written, track-colored before.
func (u *UI) genTicks(steps []GenStep, compact bool) {
	p := u.T.Palette
	height := float32(3)
	if compact {
		height = 2
	}
	row := ui.Row(u.C).FillWidth().Gap(tokens.Space1).Height(height).Key("gen-ticks")
	row.Children(func() {
		for _, step := range steps {
			tick := ui.Box(u.C).Grow(1).Height(height).Radius(height / 2).
				Background(mygo.Color(tickColor(p, step.State)))
			if step.State == GenStepActive && !compact {
				t := tick.Loop("pulse", 1200*time.Millisecond, ui.EaseInOut)
				tick.Opacity(0.55 + 0.45*float32(math.Sin(float64(t)*math.Pi)))
			}
		}
	})
}

// tickColor maps a step state onto the palette.
func tickColor(p tokens.Palette, state GenStepState) tokens.Color {
	switch state {
	case GenStepDone:
		return p.AccentDefault
	case GenStepActive:
		return p.AccentDefault.WithAlpha(0.55)
	default:
		// The pending ticks sit on the strip's own subtle background, so
		// the border color would disappear; muted text at low alpha keeps
		// them visible without competing with the accent.
		return p.TextMuted.WithAlpha(0.45)
	}
}

// genPhaseText localizes the phase.
func genPhaseText(phase GenPhase) string {
	switch phase {
	case GenPhaseThinking:
		return i18n.T("components.gen.phase_thinking")
	case GenPhaseWriting:
		return i18n.T("components.gen.phase_writing")
	case GenPhaseDone:
		return i18n.T("components.gen.phase_done")
	case GenPhaseFailed:
		return i18n.T("components.gen.phase_failed")
	default:
		return i18n.T("components.gen.phase_preparing")
	}
}

// genPhaseColor keeps the phase word neutral while work continues, so the
// panel stays quiet, and uses the status color only at the ends.
func genPhaseColor(p tokens.Palette, phase GenPhase) tokens.Color {
	switch phase {
	case GenPhaseDone:
		return p.StatusSuccess
	case GenPhaseFailed:
		return p.StatusDanger
	default:
		return p.TextSecondary
	}
}

// genCounter is "3/7 · 理论研究进展", or just the count without a name.
func genCounter(index, total int, current string) string {
	out := strconv.Itoa(index) + "/" + strconv.Itoa(total)
	if current != "" {
		out += " · " + current
	}
	return out
}

// genElapsed is the elapsed time as "42s" under a minute and "3m12s" above,
// short enough to sit at the end of the panel's first line.
func genElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return i18n.Tf("components.gen.elapsed_seconds", map[string]string{
			"s": strconv.Itoa(int(d / time.Second)),
		})
	}
	return i18n.Tf("components.gen.elapsed_minutes", map[string]string{
		"m": strconv.Itoa(int(d / time.Minute)),
		"s": strconv.Itoa(int(d/time.Second) % 60),
	})
}

// ---------------------------------------------------------------------------
// The companion
// ---------------------------------------------------------------------------

// genCompanionUnits is the coordinate space the flask is drawn in; the draw
// scales it to the size the caller asked for.
const genCompanionUnits float32 = 96

// GenCompanion draws the generation companion: a round-bottom flask that
// breathes while it waits, blinks, lets bubbles rise faster while text is
// being written, bounces once when the work is done and tilts when it failed.
// It draws nothing that takes input, so it never stands between the user and a
// control.
func (u *UI) GenCompanion(phase GenPhase, size float32) ui.Element {
	if size <= 0 {
		size = 88
	}
	p := u.T.Palette
	still := u.T.Motion.Reduced
	box := ui.Box(u.C).Key("gen-companion").Width(size).Height(size)

	var (
		bob    float32
		tilt   float32
		blink  float32 = 1
		bubble float32
		hop    float32
	)
	switch phase {
	case GenPhaseThinking, GenPhaseWriting:
		if !still {
			period := 2600 * time.Millisecond
			if phase == GenPhaseWriting {
				period = 1500 * time.Millisecond
			}
			bubble = box.Loop("bubble", period, ui.Linear)
			breath := box.Loop("breath", 2400*time.Millisecond, ui.EaseInOut)
			amp := float32(1.2)
			if phase == GenPhaseWriting {
				amp = 2.0
			}
			bob = float32(math.Sin(float64(breath)*math.Pi*2)) * amp
			blink = box.Keyframes("blink", []ui.Keyframe{
				{At: 0, Value: 1},
				{At: 3000 * time.Millisecond, Value: 1},
				{At: 3120 * time.Millisecond, Value: 0.1},
				{At: 3240 * time.Millisecond, Value: 1},
			}, ui.KeyframeOptions{Repeat: ui.RepeatForever})
		}
	case GenPhaseDone:
		if !still {
			hop = box.Keyframes("hop", []ui.Keyframe{
				{At: 0, Value: 0},
				{At: 150 * time.Millisecond, Value: -7},
				{At: 300 * time.Millisecond, Value: 0},
				{At: 430 * time.Millisecond, Value: -3},
				{At: 560 * time.Millisecond, Value: 0},
			}, ui.KeyframeOptions{Run: 1})
		}
	case GenPhaseFailed:
		blink = 0.35
		if !still {
			tilt = box.AnimateTo("tilt", -7, ui.Smooth)
			bob = 0
		} else {
			tilt = -7
		}
	}
	if phase != GenPhaseFailed {
		tilt = box.AnimateTo("tilt", 0, ui.Smooth)
	}
	box.Translate(0, bob+hop).Rotate(tilt)

	accent := p.AccentDefault
	glass := p.AccentSubtle
	ink := p.TextPrimary
	mouth := p.TextMuted
	if phase == GenPhaseFailed {
		accent = p.StatusDanger
		glass = p.BgSubtle
	}
	happy := phase == GenPhaseDone
	box.Draw(func(paint *ui.Painter, r ui.Rect) {
		if r.W <= 0 || r.H <= 0 {
			return
		}
		k := min(r.W, r.H) / genCompanionUnits
		if k <= 0 {
			return
		}
		// The pen maps the 96-unit drawing onto the box in window DIPs;
		// Painter.Transform scales around a point, which would move the
		// figure off its box.
		drawFlask(&flaskPen{
			p: paint,
			x: r.X + (r.W-genCompanionUnits*k)/2,
			y: r.Y + (r.H-genCompanionUnits*k)/2,
			k: k,
		}, glass, accent, ink, mouth, blink, bubble, happy, phase)
	})
	return box
}

// flaskPen draws the companion in its own 96-unit space.
type flaskPen struct {
	p    *ui.Painter
	x, y float32
	k    float32
}

func (f *flaskPen) rect(lx, ly, lw, lh float32) ui.Rect {
	return ui.Rect{X: f.x + lx*f.k, Y: f.y + ly*f.k, W: lw * f.k, H: lh * f.k}
}

func (f *flaskPen) px(v float32) float32 { return f.x + v*f.k }
func (f *flaskPen) py(v float32) float32 { return f.y + v*f.k }
func (f *flaskPen) w(v float32) float32  { return v * f.k }
func (f *flaskPen) fill(lx, ly, lw, lh, radius float32, c ui.Color) {
	f.p.Fill(f.rect(lx, ly, lw, lh), c, f.w(radius))
}

// drawFlask paints the character in the 96-unit space.
func drawFlask(p *flaskPen, glass, accent, ink, mouth tokens.Color, blink, bubble float32, happy bool, phase GenPhase) {
	glassC := mygo.Color(glass)
	accentC := mygo.Color(accent)
	inkC := mygo.Color(ink)
	mouthC := mygo.Color(mouth)

	// The neck and its lip.
	p.fill(39, 12, 18, 24, 3, glassC)
	p.fill(34, 7, 28, 7, 3.5, glassC)
	p.fill(35.5, 8.5, 25, 4, 2, accentC.Alpha(0.35))

	body := p.rect(18, 30, 60, 60)
	p.p.Fill(body, glassC, p.w(30))

	// The liquid, clipped to the flask so it never spills past the glass.
	top := float32(58)
	if phase == GenPhaseWriting {
		top = 56
	}
	p.p.Clip(body, p.w(30), func() {
		p.fill(18, top, 60, 92-top, 0, accentC.Alpha(0.5))
		// The surface: a thin line that makes the liquid read as liquid.
		p.fill(20, top-0.6, 56, 1.4, 0.7, accentC.Alpha(0.75))
		// Bubbles rise inside the liquid; they move faster while writing.
		if phase == GenPhaseThinking || phase == GenPhaseWriting || phase == GenPhaseDone {
			for i := range 3 {
				t := bubble + float32(i)*0.33
				t -= float32(math.Floor(float64(t)))
				y := top + (body.Y+body.H-top)/p.k*(1-t)
				r := 1.5 + 2.2*t
				p.p.Fill(p.rect(48-r+float32(i-1)*9, y-r, r*2, r*2),
					accentC.Alpha(0.75-0.45*t), p.w(r))
			}
		}
	})

	// The glass edge over the liquid.
	p.p.Stroke(body, glassC, p.w(30), max(1, p.w(1.4)))

	// The face.
	eyeY := float32(52)
	switch {
	case happy:
		drawArc(p, 37, eyeY, 5, inkC)
		drawArc(p, 59, eyeY, 5, inkC)
	case phase == GenPhaseFailed:
		p.fill(34, eyeY-0.8, 7, 1.6, 0.8, inkC)
		p.fill(55, eyeY-0.8, 7, 1.6, 0.8, inkC)
	default:
		h := 6 * max(blink, 0.12)
		p.fill(34.5, eyeY-h/2, 6, h, h/2, inkC)
		p.fill(55.5, eyeY-h/2, 6, h, h/2, inkC)
	}
	// The mouth: a smile, a flat line while working, a frown on failure.
	switch {
	case phase == GenPhaseFailed:
		path := &ui.Path{}
		path.MoveTo(p.px(42), p.py(70)).
			QuadTo(p.px(48), p.py(64), p.px(54), p.py(70))
		path.Cap(ui.RoundCap)
		p.p.StrokePath(path, max(1, p.w(1.6)), mouthC)
	case happy:
		path := &ui.Path{}
		path.MoveTo(p.px(42), p.py(65)).
			QuadTo(p.px(48), p.py(71), p.px(54), p.py(65))
		path.Cap(ui.RoundCap)
		p.p.StrokePath(path, max(1, p.w(1.8)), mouthC)
	default:
		p.fill(44, 66, 8, 1.6, 0.8, mouthC)
	}
	if phase == GenPhaseWriting {
		// Two short lines above the flask: it is writing.
		p.fill(64, 22, 12, 2.2, 1.1, accentC.Alpha(0.8))
		p.fill(64, 27, 8, 2.2, 1.1, accentC.Alpha(0.55))
	}
}

// drawArc paints one happy eye as an upward arc.
func drawArc(p *flaskPen, x, y, w float32, c ui.Color) {
	path := &ui.Path{}
	path.MoveTo(p.px(x-w/2), p.py(y+1.5)).
		QuadTo(p.px(x), p.py(y-3.5), p.px(x+w/2), p.py(y+1.5))
	path.Cap(ui.RoundCap)
	p.p.StrokePath(path, max(1, p.w(1.8)), c)
}
