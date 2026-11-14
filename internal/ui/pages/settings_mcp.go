package pages

// The MCP tab of Settings is the SPEC-11 §6.1 server page: the runtime switch,
// stdio/HTTP access, client tokens, the read-only tool catalog and retention.
// The server lifecycle belongs to boot.mcpManager; this file only renders its
// key-free state and sends one settings patch at a time.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/mcp"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// mcpToolTableRows caps the read-only catalog's viewport; the table scrolls.
const mcpToolTableRows = 12

// Form sync masks keep one card's save from discarding another card's
// unsaved draft.
const (
	mcpSyncAccess = 1 << iota
	mcpSyncHTTP
	mcpSyncRetention
	mcpSyncAll = mcpSyncAccess | mcpSyncHTTP | mcpSyncRetention
)

// settingsMCP is the MCP tab's session state. Task-written fields are guarded
// by mu; the form drafts and dialogs are written by the frame only.
type settingsMCP struct {
	mu      sync.Mutex
	loading bool
	loaded  bool

	problems []string
	values   mcp.SettingsValues
	status   MCPStatus
	tools    []MCPToolInfo
	clients  []mcp.Client
	// clientsErr is set when the token list could not be read; the previous
	// list is kept.
	clientsErr string
	bridge     string
	bridgeErr  string

	actionBusy string
	actionErr  string
	actionOK   string
	formSync   int

	clientCreateBusy bool
	clientCreateDone bool
	clientCreateErr  string
	clientDeleteBusy bool
	clientDeleteDone bool
	clientDeleteErr  string

	pendingToken       bool
	pendingTokenValue  string
	pendingTokenClient string
	pendingTokenMode   string

	// Frame-owned form drafts and dialogs.
	accessLabel  string
	httpEnabled  bool
	httpAddr     string
	httpAddrErr  string
	retention    string
	retentionErr string

	clientCreateOpen bool
	clientName       string
	clientDeleteOpen bool
	clientDeleteID   string
	clientDeleteName string

	tokenOpen   bool
	tokenValue  string
	tokenClient string
	tokenMode   string
}

// mcpData is one read of the MCP configuration.
type mcpData struct {
	values     mcp.SettingsValues
	status     MCPStatus
	tools      []MCPToolInfo
	clients    []mcp.Client
	clientsOK  bool
	clientsErr string
	bridge     string
	bridgeErr  string
	problems   []string
}

// mcpView is one frame's copy of the task-written state.
type mcpView struct {
	loaded, loading bool
	problems        []string
	values          mcp.SettingsValues
	status          MCPStatus
	tools           []MCPToolInfo
	clients         []mcp.Client
	clientsErr      string
	bridge          string
	bridgeErr       string
	actionBusy      string
	actionErr       string
	actionOK        string
	clientCreateErr string
	clientDeleteErr string
}

// mcpLoad starts the first read of the tab.
func (s *Settings) mcpLoad(a *app.App) {
	api := s.Services.MCP
	if api == nil || a == nil {
		return
	}
	s.mcp.mu.Lock()
	need := !s.mcp.loading && !s.mcp.loaded
	if need {
		s.mcp.loading = true
	}
	s.mcp.mu.Unlock()
	if !need {
		return
	}
	a.RunTask(i18n.T("pages.settings.mcp.task_load"), func(ctx context.Context, t *app.Task) error {
		data := fetchMCP(ctx, api)
		s.mcp.mu.Lock()
		s.mcp.loading = false
		s.mcp.loaded = true
		s.applyMCPDataLocked(data)
		s.mcp.formSync = mcpSyncAll
		s.mcp.mu.Unlock()
		a.Notify()
		return nil
	})
}

// fetchMCP reads every surface the tab shows. A settings read error still
// returns the loaded values so the form can show what needs fixing.
func fetchMCP(ctx context.Context, api MCPAPI) mcpData {
	var d mcpData
	var err error
	d.values, err = api.GetMCPSettings(ctx)
	if err != nil {
		d.problems = append(d.problems, errtext.Text(err))
	}
	d.status = api.Status()
	d.tools = api.Tools()
	d.clients, err = api.Clients(ctx)
	if err != nil {
		d.clientsErr = mcpClientErrText(err)
		d.problems = append(d.problems, d.clientsErr)
	} else {
		d.clientsOK = true
	}
	d.bridge, err = api.BridgeCommand()
	if err != nil {
		d.bridgeErr = errtext.Text(err)
		d.problems = append(d.problems, d.bridgeErr)
	}
	if strings.TrimSpace(d.bridge) == "" {
		d.bridge = "researchos-mcp"
	}
	return d
}

// applyMCPDataLocked installs a fetch result. Callers hold mu.
func (s *Settings) applyMCPDataLocked(d mcpData) {
	s.mcp.values = d.values
	s.mcp.status = d.status
	s.mcp.tools = d.tools
	if d.clientsOK {
		s.mcp.clients = d.clients
		s.mcp.clientsErr = ""
	} else {
		s.mcp.clientsErr = d.clientsErr
	}
	if d.bridge != "" {
		s.mcp.bridge = d.bridge
	}
	s.mcp.bridgeErr = d.bridgeErr
	s.mcp.problems = append([]string(nil), d.problems...)
}

// mcpView returns the frame's copy of the state.
func (s *Settings) mcpView() mcpView {
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	return mcpView{
		loaded: s.mcp.loaded, loading: s.mcp.loading,
		problems:   append([]string(nil), s.mcp.problems...),
		values:     s.mcp.values,
		status:     s.mcp.status,
		tools:      s.mcp.tools,
		clients:    s.mcp.clients,
		clientsErr: s.mcp.clientsErr,
		bridge:     s.mcp.bridge,
		bridgeErr:  s.mcp.bridgeErr,
		actionBusy: s.mcp.actionBusy, actionErr: s.mcp.actionErr, actionOK: s.mcp.actionOK,
		clientCreateErr: s.mcp.clientCreateErr,
		clientDeleteErr: s.mcp.clientDeleteErr,
	}
}

// mcpBusy reports any MCP operation in flight.
func (s *Settings) mcpBusy() bool {
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	return s.mcp.loading || s.mcp.actionBusy != "" ||
		s.mcp.clientCreateBusy || s.mcp.clientDeleteBusy
}

// mcpTab renders the MCP configuration page.
func (s *Settings) mcpTab(v *components.UI, a *app.App) {
	s.mcpTickState()
	api := s.Services.MCP
	if api == nil || a == nil {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.mcp.unavailable_title"),
			Message: i18n.T("pages.settings.mcp.unavailable_message"),
		})
		return
	}
	// Running/error fields change on the background start, so refresh the
	// cheap cached status every frame; tables and clients stay where they are.
	st := api.Status()
	s.mcp.mu.Lock()
	s.mcp.status = st
	s.mcp.mu.Unlock()
	view := s.mcpView()
	if !view.loaded {
		v.Skeleton(components.SkeletonOptions{Lines: 6})
		return
	}
	for _, problem := range view.problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.settings.mcp.load_problem"),
			Message: problem,
		})
	}
	s.mcpActionBanners(v, view)
	s.mcpServerCard(v, a, view)
	s.mcpStdioCard(v, a, view)
	s.mcpHTTPCard(v, a, view)
	s.mcpToolsCard(v, view)
	s.mcpRetentionCard(v, a, view)
	s.mcpDialogs(v, a)
}

// mcpActionBanners reports settings and client action results. A dialog's
// error is shown once the dialog closed (for example after a tab switch).
func (s *Settings) mcpActionBanners(v *components.UI, view mcpView) {
	if view.actionErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.mcp.action_failed"),
			Message: view.actionErr,
		})
	}
	if !s.mcp.clientCreateOpen && view.clientCreateErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.mcp.action_failed"),
			Message: view.clientCreateErr,
		})
	}
	if !s.mcp.clientDeleteOpen && view.clientDeleteErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.mcp.action_failed"),
			Message: view.clientDeleteErr,
		})
	}
	if view.actionOK != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Success,
			Title:   i18n.T("pages.settings.mcp.action_done"),
			Message: view.actionOK,
		})
	}
}

// mcpServerCard is the master switch, access mode and server facts.
func (s *Settings) mcpServerCard(v *components.UI, a *app.App, view mcpView) {
	busy := s.mcpBusy()
	p := v.T.Palette
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.mcp.card_server")}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			enabled := view.values.Enabled
			v.Switch(&enabled, i18n.T("pages.settings.mcp.enable_label"), components.ToggleOptions{Disabled: busy})
			if enabled != view.values.Enabled {
				s.mcpToggleEnabled(a, enabled)
			}
			tone, text := components.Neutral, i18n.T("pages.settings.mcp.status_stopped")
			switch {
			case !view.values.Enabled:
				tone, text = components.Neutral, i18n.T("pages.settings.mcp.status_disabled")
			case view.status.Running:
				tone, text = components.Success, i18n.T("pages.settings.mcp.status_running")
			case view.status.Error != "":
				tone, text = components.Danger, i18n.T("pages.settings.mcp.status_failed")
			}
			v.StatusIndicator(tone, text)
			if view.status.Error != "" {
				ui.Text(v.C, view.status.Error).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.StatusDanger))
			}
			v.Divider()
			if s.mcp.accessLabel == "" {
				s.mcp.accessLabel = mcpAccessLabel(view.values.AccessMode)
			}
			v.Select(&s.mcp.accessLabel, components.SelectOptions{
				Label:    i18n.T("pages.settings.mcp.access_label"),
				Options:  []string{i18n.T("pages.settings.mcp.access_read_only"), i18n.T("pages.settings.mcp.access_read_write")},
				Disabled: busy,
			})
			ui.Text(v.C, i18n.T("pages.settings.mcp.access_hint")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextSecondary))
			v.Button(i18n.T("pages.settings.mcp.btn_save_access"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Disabled: busy,
				OnClick: func() { s.mcpSaveAccess(a) },
			})
			v.Divider()
			s.mcpRow(v, i18n.T("pages.settings.mcp.row_version"), valueOrDash(view.status.Version))
			s.mcpRow(v, i18n.T("pages.settings.mcp.row_protocols"), strings.Join(view.status.Protocols, "、"))
			s.mcpRow(v, i18n.T("pages.settings.mcp.row_workspace"), valueOrDash(view.status.WorkspaceRoot))
			moduleText := strconv.Itoa(len(view.status.Modules))
			if len(view.status.Modules) > 0 {
				moduleText += " · " + strings.Join(view.status.Modules, "、")
			}
			s.mcpRow(v, i18n.T("pages.settings.mcp.row_modules"), moduleText)
			s.mcpRow(v, i18n.T("pages.settings.mcp.row_tools"), strconv.Itoa(view.status.ToolCount))
			if view.status.Socket != "" {
				s.mcpRow(v, i18n.T("pages.settings.mcp.row_socket"), view.status.Socket)
			}
		})
	})
}

// mcpStdioCard shows the bridge command and the Claude Desktop snippet.
func (s *Settings) mcpStdioCard(v *components.UI, a *app.App, view mcpView) {
	p := v.T.Palette
	bridge := view.bridge
	if strings.TrimSpace(bridge) == "" {
		bridge = "researchos-mcp"
	}
	command := mcpShellCommand(bridge) + " --stdio"
	snippet := mcpClaudeSnippet(bridge)
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.mcp.card_stdio")}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, i18n.T("pages.settings.mcp.stdio_desc")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextSecondary))
			ui.Text(v.C, i18n.T("pages.settings.mcp.stdio_running_hint")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
			ui.Text(v.C, i18n.T("pages.settings.mcp.stdio_cmd_label")).
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightSemibold))
			s.mcpCode(v, command)
			v.Button(i18n.T("pages.settings.mcp.btn_copy_command"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Icon: icons.Copy,
				OnClick: func() { s.mcpCopy(a, v, command) },
			})
			ui.Text(v.C, i18n.T("pages.settings.mcp.stdio_json_label")).
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightSemibold))
			s.mcpCode(v, snippet)
			v.Button(i18n.T("pages.settings.mcp.btn_copy_json"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Icon: icons.Copy,
				OnClick: func() { s.mcpCopy(a, v, snippet) },
			})
			if bridge == "researchos-mcp" {
				ui.Text(v.C, i18n.T("pages.settings.mcp.stdio_fallback")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.StatusWarning))
			}
		})
	})
}

// mcpHTTPCard is the listener form and the client token table.
func (s *Settings) mcpHTTPCard(v *components.UI, a *app.App, view mcpView) {
	busy := s.mcpBusy()
	p := v.T.Palette
	v.Card(components.CardOptions{
		Title: i18n.T("pages.settings.mcp.card_http"),
		Actions: func() {
			v.Button(i18n.T("pages.settings.mcp.btn_new_token"), components.ButtonOptions{
				Variant: components.VariantPrimary, Size: components.SizeSmall, Icon: icons.Plus,
				Disabled: busy,
				OnClick:  func() { s.mcpOpenClientDialog() },
			})
		},
	}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			v.Switch(&s.mcp.httpEnabled, i18n.T("pages.settings.mcp.http_enable_label"), components.ToggleOptions{Disabled: busy})
			addrIn := v.Input(&s.mcp.httpAddr, components.FieldOptions{
				Label:       i18n.T("pages.settings.mcp.http_addr_label"),
				Placeholder: mcp.DefaultHTTPAddr,
				Error:       s.mcp.httpAddrErr,
				Description: i18n.T("pages.settings.mcp.http_addr_hint"),
				Disabled:    busy,
			})
			if addrIn.Changed() {
				s.mcp.httpAddrErr = ""
			}
			v.Button(i18n.T("pages.settings.mcp.btn_save_http"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Disabled: busy,
				OnClick: func() { s.mcpSaveHTTP(a) },
			})
			if view.values.HTTPEnabled {
				s.mcpRow(v, i18n.T("pages.settings.mcp.http_endpoint_label"), valueOrDash(view.status.Endpoint))
				ui.Text(v.C, i18n.T("pages.settings.mcp.http_endpoint_hint")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.TextMuted))
			}
			v.Divider()
			ui.Text(v.C, i18n.T("pages.settings.mcp.http_token_hint")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextSecondary))
			if view.clientsErr != "" {
				ui.Text(v.C, view.clientsErr).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.StatusDanger))
			}
			if len(view.clients) == 0 {
				ui.Text(v.C, i18n.T("pages.settings.mcp.clients_empty")).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextMuted))
				return
			}
			rows := view.clients
			table := v.Table(components.TableOptions{
				Columns: []components.Column{
					{Title: i18n.T("pages.settings.mcp.col_client_name"), MinWidth: 140},
					{Title: i18n.T("pages.settings.mcp.col_client_created"), Width: 132},
					{Title: i18n.T("pages.settings.mcp.col_client_last_seen"), Width: 132},
					{Title: i18n.T("pages.settings.mcp.col_client_enabled"), Width: 74},
					{Title: i18n.T("pages.settings.mcp.col_client_actions"), Width: 220},
				},
				Rows: len(rows),
				Key:  func(i int) any { return rows[i].ID },
				Cell: func(row, col int) {
					c := rows[row]
					switch col {
					case 0:
						ui.Text(v.C, c.Name).SingleLine()
					case 1:
						ui.Text(v.C, c.CreatedAt.Local().Format("2006-01-02 15:04")).FontFeatures("tnum")
					case 2:
						if c.LastSeenAt == nil {
							ui.Text(v.C, "—").TextColor(mygo.Color(p.TextMuted))
						} else {
							ui.Text(v.C, c.LastSeenAt.Local().Format("2006-01-02 15:04")).FontFeatures("tnum")
						}
					case 3:
						if c.Enabled {
							v.Badge(i18n.T("pages.settings.mcp.client_enabled"), components.Success)
						} else {
							v.Badge(i18n.T("pages.settings.mcp.client_disabled"), components.Neutral)
						}
					case 4:
						ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
							v.Button(i18n.T("pages.settings.mcp.btn_rotate"), components.ButtonOptions{
								Variant: components.VariantGhost, Size: components.SizeSmall,
								Disabled: busy, Loading: view.actionBusy == "client-rotate:"+c.ID,
								OnClick: func() { s.mcpRotateClient(a, c) },
							})
							v.Button(i18n.T("pages.settings.mcp.btn_revoke"), components.ButtonOptions{
								Variant: components.VariantGhost, Size: components.SizeSmall,
								Disabled: busy,
								OnClick:  func() { s.mcpAskRevoke(c) },
							})
							label := i18n.T("pages.settings.mcp.btn_enable")
							if c.Enabled {
								label = i18n.T("pages.settings.mcp.btn_disable")
							}
							v.Button(label, components.ButtonOptions{
								Variant: components.VariantGhost, Size: components.SizeSmall,
								Disabled: busy, Loading: view.actionBusy == "client-toggle:"+c.ID,
								OnClick: func() { s.mcpSetClientEnabled(a, c, !c.Enabled) },
							})
						})
					}
				},
			})
			table.Height(tokens.Space8 + tokens.Space11*float32(len(rows)))
		})
	})
}

// mcpToolsCard is the read-only tool catalog.
func (s *Settings) mcpToolsCard(v *components.UI, view mcpView) {
	p := v.T.Palette
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.mcp.card_tools")}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, i18n.Tf("pages.settings.mcp.tools_count", map[string]string{
				"n": strconv.Itoa(len(view.tools)),
			})).FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(p.TextSecondary))
			if len(view.tools) == 0 {
				ui.Text(v.C, i18n.T("pages.settings.mcp.tools_empty")).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(p.TextMuted))
				return
			}
			rows := view.tools
			table := v.Table(components.TableOptions{
				Columns: []components.Column{
					{Title: i18n.T("pages.settings.mcp.col_tool_name"), MinWidth: 180},
					{Title: i18n.T("pages.settings.mcp.col_tool_module"), Width: 100},
					{Title: i18n.T("pages.settings.mcp.col_tool_category"), Width: 64},
					{Title: i18n.T("pages.settings.mcp.col_tool_desc"), MinWidth: 280},
					{Title: i18n.T("pages.settings.mcp.col_tool_markers"), Width: 150},
				},
				Rows: len(rows),
				Key:  func(i int) any { return rows[i].Name },
				Cell: func(row, col int) {
					tool := rows[row]
					switch col {
					case 0:
						ui.Text(v.C, tool.Name).SingleLine()
					case 1:
						ui.Text(v.C, tool.Module).SingleLine()
					case 2:
						v.Badge(tool.Category, mcpCategoryTone(tool.Category))
					case 3:
						desc := tool.Description
						if strings.TrimSpace(desc) == "" {
							desc = tool.Title
						}
						ui.Text(v.C, desc).FontSize(tokens.Typography.Body.Size).MaxLines(2)
					case 4:
						ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
							if tool.ReadOnly {
								v.Badge(i18n.T("pages.settings.mcp.tool_read_only"), components.Success)
							}
							if tool.Destructive {
								v.Badge(i18n.T("pages.settings.mcp.tool_destructive"), components.Warning)
							}
							if !tool.ReadOnly && !tool.Destructive {
								ui.Text(v.C, "—").TextColor(mygo.Color(p.TextMuted))
							}
						})
					}
				},
			})
			heightRows := len(rows)
			if heightRows > mcpToolTableRows {
				heightRows = mcpToolTableRows
			}
			table.Height(tokens.Space8 + tokens.Space11*float32(heightRows))
		})
	})
}

// mcpRetentionCard edits the runtime-record retention window.
func (s *Settings) mcpRetentionCard(v *components.UI, a *app.App, view mcpView) {
	busy := s.mcpBusy()
	p := v.T.Palette
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.mcp.card_retention")}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			in := v.Input(&s.mcp.retention, components.FieldOptions{
				Label:       i18n.T("pages.settings.mcp.retention_label"),
				Description: i18n.T("pages.settings.mcp.retention_hint"),
				Error:       s.mcp.retentionErr,
				Disabled:    busy,
			})
			if in.Changed() {
				s.mcp.retentionErr = ""
			}
			v.Button(i18n.T("pages.settings.mcp.btn_save_retention"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Disabled: busy,
				OnClick: func() { s.mcpSaveRetention(a) },
			})
			if view.values.ConfirmationTTL > 0 {
				s.mcpRow(v, i18n.T("pages.settings.mcp.ttl_label"), formatMCPTTL(view.values.ConfirmationTTL))
			}
			ui.Text(v.C, i18n.T("pages.settings.mcp.retention_scope")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
		})
	})
}

// mcpToggleEnabled applies the master switch immediately.
func (s *Settings) mcpToggleEnabled(a *app.App, enabled bool) {
	if s.mcpBusy() {
		return
	}
	s.mcp.mu.Lock()
	s.mcp.values.Enabled = enabled
	s.mcp.mu.Unlock()
	s.mcpApply(a, MCPSettingsPatch{Enabled: &enabled}, "enabled", 0)
}

// mcpSaveAccess applies the access mode.
func (s *Settings) mcpSaveAccess(a *app.App) {
	if s.mcpBusy() {
		return
	}
	mode, ok := mcpAccessValue(s.mcp.accessLabel)
	if !ok {
		s.mcp.mu.Lock()
		s.mcp.actionErr = i18n.T("pages.settings.mcp.error.access")
		s.mcp.mu.Unlock()
		return
	}
	s.mcpApply(a, MCPSettingsPatch{AccessMode: &mode}, "access", mcpSyncAccess)
}

// mcpSaveHTTP validates the loopback address and applies the HTTP settings.
func (s *Settings) mcpSaveHTTP(a *app.App) {
	if s.mcpBusy() {
		return
	}
	addr := strings.TrimSpace(s.mcp.httpAddr)
	if err := mcp.ValidateListenAddr(addr); err != nil {
		s.mcp.httpAddrErr = i18n.T("pages.settings.mcp.error.addr")
		return
	}
	s.mcp.httpAddrErr = ""
	enabled := s.mcp.httpEnabled
	s.mcpApply(a, MCPSettingsPatch{HTTPEnabled: &enabled, HTTPAddr: &addr}, "http", mcpSyncHTTP)
}

// mcpSaveRetention validates and applies the retention days.
func (s *Settings) mcpSaveRetention(a *app.App) {
	if s.mcpBusy() {
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(s.mcp.retention))
	if err != nil || days < mcp.MinRetentionDays || days > mcp.MaxRetentionDays {
		s.mcp.retentionErr = i18n.T("pages.settings.mcp.error.retention")
		return
	}
	s.mcp.retentionErr = ""
	s.mcpApply(a, MCPSettingsPatch{RetentionDays: &days}, "retention", mcpSyncRetention)
}

// mcpApply persists a patch and refreshes the page state in one task.
func (s *Settings) mcpApply(a *app.App, patch MCPSettingsPatch, busy string, syncMask int) {
	api := s.Services.MCP
	if api == nil || a == nil || s.mcpBusy() {
		return
	}
	s.mcp.mu.Lock()
	s.mcp.actionBusy = busy
	s.mcp.actionErr, s.mcp.actionOK = "", ""
	s.mcp.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.mcp.task_apply"), func(ctx context.Context, t *app.Task) error {
		values, saveErr := api.SaveMCPSettings(ctx, patch)
		data := fetchMCP(ctx, api)
		s.mcp.mu.Lock()
		s.mcp.actionBusy = ""
		if saveErr != nil {
			s.mcp.actionErr = errtext.Text(saveErr)
			s.mcp.values = data.values
		} else {
			s.mcp.actionOK = i18n.T("pages.settings.mcp.saved")
			s.mcp.values = values
		}
		s.mcp.status = data.status
		s.mcp.tools = data.tools
		if data.clientsOK {
			s.mcp.clients = data.clients
			s.mcp.clientsErr = ""
		} else {
			s.mcp.clientsErr = data.clientsErr
		}
		if data.bridge != "" {
			s.mcp.bridge = data.bridge
		}
		s.mcp.bridgeErr = data.bridgeErr
		s.mcp.problems = append([]string(nil), data.problems...)
		if syncMask != 0 {
			s.mcp.formSync = syncMask
		}
		s.mcp.mu.Unlock()
		a.Notify()
		return nil
	})
}

// mcpTickState moves task completion flags into frame state.
func (s *Settings) mcpTickState() {
	s.mcp.mu.Lock()
	values := s.mcp.values
	formSync := s.mcp.formSync
	createDone, deleteDone := s.mcp.clientCreateDone, s.mcp.clientDeleteDone
	pending := s.mcp.pendingToken
	tokenValue, tokenClient, tokenMode := s.mcp.pendingTokenValue, s.mcp.pendingTokenClient, s.mcp.pendingTokenMode
	if pending {
		s.mcp.pendingToken = false
		s.mcp.pendingTokenValue, s.mcp.pendingTokenClient, s.mcp.pendingTokenMode = "", "", ""
	}
	s.mcp.formSync = 0
	s.mcp.clientCreateDone, s.mcp.clientDeleteDone = false, false
	s.mcp.mu.Unlock()

	if formSync&mcpSyncAccess != 0 {
		s.mcp.accessLabel = mcpAccessLabel(values.AccessMode)
	}
	if formSync&mcpSyncHTTP != 0 {
		s.mcp.httpEnabled = values.HTTPEnabled
		s.mcp.httpAddr = values.HTTPAddr
		s.mcp.httpAddrErr = ""
	}
	if formSync&mcpSyncRetention != 0 {
		s.mcp.retention = strconv.Itoa(values.RetentionDays)
		s.mcp.retentionErr = ""
	}
	if createDone {
		s.mcp.clientCreateOpen = false
		s.mcp.clientName = ""
	}
	if deleteDone {
		s.mcp.clientDeleteOpen = false
	}
	if pending {
		s.mcp.tokenValue = tokenValue
		s.mcp.tokenClient = tokenClient
		s.mcp.tokenMode = tokenMode
		s.mcp.tokenOpen = true
	}
	if !s.mcp.tokenOpen {
		s.mcp.tokenValue = ""
	}
}

// mcpClearTransient closes the tab's dialogs and drops any revealed token when
// another Settings tab is shown.
func (s *Settings) mcpClearTransient() {
	s.mcp.clientCreateOpen = false
	s.mcp.clientDeleteOpen = false
	s.mcp.tokenOpen = false
	s.mcp.tokenValue = ""
	s.mcp.tokenClient = ""
	s.mcp.tokenMode = ""
	s.mcp.clientName = ""
	s.mcp.httpAddrErr = ""
	s.mcp.retentionErr = ""
	// Re-read the persisted values into the drafts on the next visit, so a
	// language switch cannot leave stale select labels behind.
	s.mcp.mu.Lock()
	s.mcp.formSync = mcpSyncAll
	s.mcp.mu.Unlock()
}

// mcpOpenClientDialog opens the new-token dialog.
func (s *Settings) mcpOpenClientDialog() {
	if s.mcpBusy() {
		return
	}
	s.mcp.mu.Lock()
	s.mcp.clientCreateErr = ""
	s.mcp.clientCreateDone = false
	s.mcp.mu.Unlock()
	s.mcp.clientName = ""
	s.mcp.clientCreateOpen = true
}

// mcpCreateClient creates a client and reveals its one-time token.
func (s *Settings) mcpCreateClient(a *app.App) {
	api := s.Services.MCP
	if api == nil || a == nil || s.mcpBusy() {
		return
	}
	name := strings.TrimSpace(s.mcp.clientName)
	if name == "" {
		s.mcp.mu.Lock()
		s.mcp.clientCreateErr = i18n.T("pages.settings.mcp.error.client_name")
		s.mcp.mu.Unlock()
		return
	}
	s.mcp.mu.Lock()
	s.mcp.clientCreateBusy = true
	s.mcp.clientCreateErr = ""
	s.mcp.clientCreateDone = false
	s.mcp.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.mcp.task_create_client"), func(ctx context.Context, t *app.Task) error {
		client, token, err := api.CreateClient(ctx, name)
		var clients []mcp.Client
		var clientsErr error
		if err == nil {
			clients, clientsErr = api.Clients(ctx)
		}
		s.mcp.mu.Lock()
		s.mcp.clientCreateBusy = false
		if err != nil {
			s.mcp.clientCreateErr = mcpClientErrText(err)
		} else {
			s.mcp.clientCreateDone = true
			s.mcp.pendingToken = true
			s.mcp.pendingTokenValue = token
			s.mcp.pendingTokenClient = client.Name
			s.mcp.pendingTokenMode = "create"
			if clientsErr == nil {
				s.mcp.clients = clients
				s.mcp.clientsErr = ""
			} else {
				s.mcp.clientsErr = mcpClientErrText(clientsErr)
			}
		}
		s.mcp.mu.Unlock()
		a.Notify()
		return nil
	})
}

// mcpRotateClient replaces one client token and reveals the new value once.
func (s *Settings) mcpRotateClient(a *app.App, c mcp.Client) {
	api := s.Services.MCP
	if api == nil || a == nil || s.mcpBusy() {
		return
	}
	busy := "client-rotate:" + c.ID
	s.mcp.mu.Lock()
	s.mcp.actionBusy = busy
	s.mcp.actionErr, s.mcp.actionOK = "", ""
	s.mcp.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.mcp.task_rotate_client"), func(ctx context.Context, t *app.Task) error {
		token, err := api.RotateClient(ctx, c.ID)
		var clients []mcp.Client
		var clientsErr error
		if err == nil {
			clients, clientsErr = api.Clients(ctx)
		}
		s.mcp.mu.Lock()
		s.mcp.actionBusy = ""
		if err != nil {
			s.mcp.actionErr = mcpClientErrText(err)
		} else {
			s.mcp.pendingToken = true
			s.mcp.pendingTokenValue = token
			s.mcp.pendingTokenClient = c.Name
			s.mcp.pendingTokenMode = "rotate"
			if clientsErr == nil {
				s.mcp.clients = clients
				s.mcp.clientsErr = ""
			} else {
				s.mcp.clientsErr = mcpClientErrText(clientsErr)
			}
		}
		s.mcp.mu.Unlock()
		a.Notify()
		return nil
	})
}

// mcpSetClientEnabled toggles one client token.
func (s *Settings) mcpSetClientEnabled(a *app.App, c mcp.Client, enabled bool) {
	api := s.Services.MCP
	if api == nil || a == nil || s.mcpBusy() {
		return
	}
	busy := "client-toggle:" + c.ID
	s.mcp.mu.Lock()
	s.mcp.actionBusy = busy
	s.mcp.actionErr, s.mcp.actionOK = "", ""
	s.mcp.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.mcp.task_toggle_client"), func(ctx context.Context, t *app.Task) error {
		err := api.SetClientEnabled(ctx, c.ID, enabled)
		var clients []mcp.Client
		var clientsErr error
		if err == nil {
			clients, clientsErr = api.Clients(ctx)
		}
		s.mcp.mu.Lock()
		s.mcp.actionBusy = ""
		switch {
		case err != nil:
			s.mcp.actionErr = mcpClientErrText(err)
		default:
			if enabled {
				s.mcp.actionOK = i18n.T("pages.settings.mcp.client_enabled_ok")
			} else {
				s.mcp.actionOK = i18n.T("pages.settings.mcp.client_disabled_ok")
			}
			if clientsErr == nil {
				s.mcp.clients = clients
				s.mcp.clientsErr = ""
			} else {
				s.mcp.clientsErr = mcpClientErrText(clientsErr)
			}
		}
		s.mcp.mu.Unlock()
		a.Notify()
		return nil
	})
}

// mcpAskRevoke opens the revocation confirmation.
func (s *Settings) mcpAskRevoke(c mcp.Client) {
	if s.mcpBusy() {
		return
	}
	s.mcp.mu.Lock()
	s.mcp.clientDeleteErr = ""
	s.mcp.clientDeleteDone = false
	s.mcp.mu.Unlock()
	s.mcp.clientDeleteID = c.ID
	s.mcp.clientDeleteName = c.Name
	s.mcp.clientDeleteOpen = true
}

// mcpRevokeClient revokes the confirmed client.
func (s *Settings) mcpRevokeClient(a *app.App) {
	api := s.Services.MCP
	if api == nil || a == nil || s.mcpBusy() {
		return
	}
	id := s.mcp.clientDeleteID
	if id == "" {
		return
	}
	s.mcp.mu.Lock()
	s.mcp.clientDeleteBusy = true
	s.mcp.clientDeleteErr = ""
	s.mcp.clientDeleteDone = false
	s.mcp.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.mcp.task_revoke_client"), func(ctx context.Context, t *app.Task) error {
		err := api.RevokeClient(ctx, id)
		var clients []mcp.Client
		var clientsErr error
		if err == nil {
			clients, clientsErr = api.Clients(ctx)
		}
		s.mcp.mu.Lock()
		s.mcp.clientDeleteBusy = false
		if err != nil {
			s.mcp.clientDeleteErr = mcpClientErrText(err)
		} else {
			s.mcp.clientDeleteDone = true
			s.mcp.actionOK = i18n.T("pages.settings.mcp.client_revoked_ok")
			if clientsErr == nil {
				s.mcp.clients = clients
				s.mcp.clientsErr = ""
			} else {
				s.mcp.clientsErr = mcpClientErrText(clientsErr)
			}
		}
		s.mcp.mu.Unlock()
		a.Notify()
		return nil
	})
}

// mcpDialogs renders the client-token dialogs.
func (s *Settings) mcpDialogs(v *components.UI, a *app.App) {
	s.mcpClientDialog(v, a)
	s.mcpTokenDialog(v, a)
	s.mcpRevokeDialog(v, a)
}

// mcpClientDialog names and creates a client.
func (s *Settings) mcpClientDialog(v *components.UI, a *app.App) {
	if !s.mcp.clientCreateOpen {
		return
	}
	s.mcp.mu.Lock()
	busy, createErr := s.mcp.clientCreateBusy, s.mcp.clientCreateErr
	s.mcp.mu.Unlock()
	v.Dialog(&s.mcp.clientCreateOpen, components.DialogOptions{
		Title:       i18n.T("pages.settings.mcp.client_dialog_title"),
		Description: i18n.T("pages.settings.mcp.client_dialog_desc"),
		Width:       tokens.Space12 * 9,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.mcp.clientCreateOpen = false
			}
			if v.Button(i18n.T("pages.settings.mcp.btn_create_client"), components.ButtonOptions{
				Variant: components.VariantPrimary, Loading: busy, Disabled: busy,
			}).Clicked() {
				s.mcpCreateClient(a)
			}
		},
	}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			nameIn := v.Input(&s.mcp.clientName, components.FieldOptions{
				Label:       i18n.T("pages.settings.mcp.client_name_label"),
				Placeholder: i18n.T("pages.settings.mcp.client_name_hint"),
				AutoFocus:   true,
				Disabled:    busy,
			})
			if nameIn.Changed() {
				s.mcp.mu.Lock()
				s.mcp.clientCreateErr = ""
				s.mcp.mu.Unlock()
			}
			if createErr != "" {
				ui.Text(v.C, createErr).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.StatusDanger))
			}
		})
	})
}

// mcpTokenDialog reveals a token once. Closing it clears the value.
func (s *Settings) mcpTokenDialog(v *components.UI, a *app.App) {
	if !s.mcp.tokenOpen {
		return
	}
	key := "pages.settings.mcp.token_dialog_created"
	if s.mcp.tokenMode == "rotate" {
		key = "pages.settings.mcp.token_dialog_rotated"
	}
	desc := i18n.Tf(key, map[string]string{"name": s.mcp.tokenClient})
	v.Dialog(&s.mcp.tokenOpen, components.DialogOptions{
		Title:       i18n.T("pages.settings.mcp.token_dialog_title"),
		Description: i18n.T("pages.settings.mcp.token_dialog_desc"),
		Width:       tokens.Space12 * 12,
		Actions: func() {
			if v.Button(i18n.T("pages.settings.mcp.btn_close"), components.ButtonOptions{}).Clicked() {
				s.mcp.tokenOpen = false
			}
		},
	}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, desc).FontSize(tokens.Typography.Body.Size)
			s.mcpCode(v, s.mcp.tokenValue)
			v.Button(i18n.T("pages.settings.mcp.btn_copy_token"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Icon: icons.Copy,
				OnClick: func() { s.mcpCopy(a, v, s.mcp.tokenValue) },
			})
			ui.Text(v.C, i18n.T("pages.settings.mcp.token_dialog_once")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.StatusWarning))
		})
	})
}

// mcpRevokeDialog confirms a client revocation.
func (s *Settings) mcpRevokeDialog(v *components.UI, a *app.App) {
	if !s.mcp.clientDeleteOpen {
		return
	}
	s.mcp.mu.Lock()
	busy, revokeErr := s.mcp.clientDeleteBusy, s.mcp.clientDeleteErr
	s.mcp.mu.Unlock()
	v.Dialog(&s.mcp.clientDeleteOpen, components.DialogOptions{
		Title:       i18n.T("pages.settings.mcp.revoke_title"),
		Description: i18n.Tf("pages.settings.mcp.revoke_message", map[string]string{"name": s.mcp.clientDeleteName}),
		Width:       tokens.Space12 * 9,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.mcp.clientDeleteOpen = false
			}
			if v.Button(i18n.T("pages.settings.mcp.btn_revoke"), components.ButtonOptions{
				Variant: components.VariantDanger, Loading: busy, Disabled: busy,
			}).Clicked() {
				s.mcpRevokeClient(a)
			}
		},
	}, func() {
		if revokeErr != "" {
			ui.Text(v.C, revokeErr).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
}

// mcpClientErrText localizes the client registry's typed errors. The service
// messages are Chinese; the UI shows the same states in its own language.
func mcpClientErrText(err error) string {
	if errors.Is(err, ErrMCPUnavailable) || errors.Is(err, ErrMCPClosed) {
		return i18n.T("pages.settings.mcp.unavailable_message")
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		switch me.Code {
		case mcp.CodeNotFound:
			return i18n.T("pages.settings.mcp.error.client_missing")
		case mcp.CodeInvalidArgument:
			return i18n.T("pages.settings.mcp.error.client_name")
		default:
			return i18n.T("pages.settings.mcp.error.client")
		}
	}
	return errtext.Text(err)
}

// mcpCopy writes text to the clipboard and confirms it.
func (s *Settings) mcpCopy(a *app.App, v *components.UI, text string) {
	if text == "" {
		return
	}
	v.C.WriteClipboard(text)
	if a != nil {
		a.Toast(app.LevelSuccess, i18n.T("pages.settings.mcp.toast_copied_title"),
			i18n.T("pages.settings.mcp.toast_copied_message"))
	}
}

// mcpCode is a read-only monospace block.
func (s *Settings) mcpCode(v *components.UI, text string) {
	p := v.T.Palette
	ui.Row(v.C).FillWidth().
		Padding(tokens.Space3, tokens.PanelPadding).
		Background(mygo.Color(p.BgSubtle)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusMD).
		Children(func() {
			ui.Text(v.C, text).
				Font(tokens.SystemFonts.Mono).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextPrimary))
		})
}

// mcpRow renders a label and value pair.
func (s *Settings) mcpRow(v *components.UI, label, value string) {
	p := v.T.Palette
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		ui.Text(v.C, label).
			Width(140).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(p.TextSecondary))
		ui.Text(v.C, valueOrDash(value)).
			Grow(1).
			FontSize(tokens.Typography.Body.Size).
			TextColor(mygo.Color(p.TextPrimary))
	})
}

// mcpAccessLabel maps the stored mode to the select label.
func mcpAccessLabel(mode string) string {
	if mode == mcp.AccessReadOnly {
		return i18n.T("pages.settings.mcp.access_read_only")
	}
	return i18n.T("pages.settings.mcp.access_read_write")
}

// mcpAccessValue maps a select label back to the stored mode.
func mcpAccessValue(label string) (string, bool) {
	switch label {
	case i18n.T("pages.settings.mcp.access_read_only"):
		return mcp.AccessReadOnly, true
	case i18n.T("pages.settings.mcp.access_read_write"):
		return mcp.AccessReadWrite, true
	}
	return "", false
}

// mcpCategoryTone colors a permission class; the letter carries the meaning.
func mcpCategoryTone(category string) components.Tone {
	switch category {
	case "W":
		return components.Accent
	case "C":
		return components.Warning
	case "J":
		return components.Info
	}
	return components.Neutral
}

// mcpShellCommand quotes a path with spaces for display and copying.
func mcpShellCommand(path string) string {
	if strings.ContainsAny(path, " \t") {
		return "\"" + path + "\""
	}
	return path
}

// mcpClaudeSnippet builds the exact Claude Desktop configuration. JSON
// escaping keeps a Windows path valid.
func mcpClaudeSnippet(bridge string) string {
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	payload := struct {
		Servers map[string]server `json:"mcpServers"`
	}{
		Servers: map[string]server{
			"researchos": {Command: bridge, Args: []string{"--stdio"}},
		},
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}

// formatMCPTTL shows the confirmation token lifetime.
func formatMCPTTL(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d%time.Minute == 0 {
		return i18n.Tf("pages.settings.mcp.ttl_minutes", map[string]string{
			"minutes": strconv.Itoa(int(d / time.Minute)),
		})
	}
	return i18n.Tf("pages.settings.mcp.ttl_seconds", map[string]string{
		"seconds": strconv.Itoa(int(d / time.Second)),
	})
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
