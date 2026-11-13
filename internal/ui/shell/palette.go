package shell

import (
	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// togglePalette opens or closes the command palette. Opening clears the
// previous query so the full command list shows.
func (s *Shell) togglePalette() {
	if s.app.PaletteOpen() {
		s.app.SetPalette(false)
		return
	}
	s.paletteQuery = ""
	s.paletteSel = 0
	s.app.SetPalette(true)
}

// palette builds the command palette over a dimmed window. Its input takes the
// focus, Enter runs the first matching command, and Escape or a press outside
// closes it.
func (s *Shell) palette(v *components.UI) {
	if !s.app.PaletteOpen() {
		return
	}
	open := true
	ui.DialogBase(v.C, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(mygo.Color(tokens.Scrim))
		panel.Width(tokens.Space12*12).
			Padding(tokens.Space3).
			Gap(tokens.Space2).
			Radius(tokens.RadiusXL).
			Background(mygo.Color(v.T.Palette.BgSurface)).
			Border(1, mygo.Color(v.T.Palette.BorderDefault)).
			Shadow(0, 8, 24, 0, mygo.Color(tokens.Shadow))

		query := s.paletteQuery
		field := ui.TextInput(v.C, &query).
			Placeholder(i18n.T("shell.palette.placeholder")).
			Label(i18n.T("shell.palette.title")).
			AutoFocus()
		if field.Changed() {
			s.paletteQuery = query
			s.paletteSel = 0
		}
		cmds := filterCommands(s.commands(), s.paletteQuery)
		if v.C.Shortcut(0, ui.KeyDown) {
			s.paletteSel++
		}
		if v.C.Shortcut(0, ui.KeyUp) {
			s.paletteSel--
		}
		if s.paletteSel >= len(cmds) {
			s.paletteSel = len(cmds) - 1
		}
		if s.paletteSel < 0 {
			s.paletteSel = 0
		}
		submitted := field.Submitted()
		ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
			if len(cmds) == 0 {
				ui.Text(v.C, i18n.T("shell.palette.no_results")).
					Padding(tokens.Space2).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted))
				return
			}
			for i, cmd := range cmds {
				if i >= paletteLimit {
					break
				}
				c := cmd
				v.CommandItem(components.CommandItemOptions{
					ID:          c.id,
					Title:       c.title,
					Hint:        c.hint,
					Icon:        c.icon,
					Highlighted: i == s.paletteSel,
					OnClick:     func() { s.runCommand(c) },
				})
			}
		})
		if submitted && len(cmds) > 0 && s.paletteSel < len(cmds) {
			s.runCommand(cmds[s.paletteSel])
		}
	})
	if !open {
		// Escape or a press outside closed it.
		s.app.SetPalette(false)
	}
}

// paletteLimit caps how many commands show at once; the search narrows the
// rest.
const paletteLimit = 8

// filterCommands keeps the commands matching query in their title or keywords.
func filterCommands(all []command, query string) []command {
	if query == "" {
		return all
	}
	out := make([]command, 0, len(all))
	for _, c := range all {
		if matches(query, c.title, c.keywords+" "+c.hint) {
			out = append(out, c)
		}
	}
	return out
}

// toastTone maps a feedback level to a status tone.
func toastTone(l app.Level) components.Tone {
	switch l {
	case app.LevelSuccess:
		return components.Success
	case app.LevelWarning:
		return components.Warning
	case app.LevelDanger:
		return components.Danger
	}
	return components.Info
}
