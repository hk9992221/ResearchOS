package boot

// The Weekly Review page reaches SPEC-09 through this adapter, the same way the
// other pages reach their module: the composition root builds the service, the
// page only calls the narrow interface. Construction mirrors the MCP binding
// (internal/mcpbind/weekly.go) so the two entry points behave the same, and
// every call acts as weeklyreview.ActorUser (the package declares its own Actor
// type; events.Actor is never used here).
//
// The SPEC-02 generation service is built per call from the same project
// handle, exactly as the MCP binding does. stack is nil while the composition
// root has not built the AI stack: weeklyreview then documents its
// deterministic skeleton generation, and the page says so instead of failing.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"researchos/internal/ai/output"
	"researchos/internal/ai/wire"
	"researchos/internal/foundation/settings"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ui/pages"
	"researchos/internal/weeklyreview"
)

// weeklyModule serves one open workspace.
type weeklyModule struct {
	ws       *fworkspace.Workspace
	stack    *wire.Stack
	settings *settings.Store
	// ready reports whether a chat provider is assigned; nil disables AI
	// generation and leaves the deterministic skeleton path.
	ready func() bool
}

var _ pages.WeeklyAPI = (*weeklyModule)(nil)

func newWeeklyModule(ws *fworkspace.Workspace, stack *wire.Stack, settings *settings.Store, ready func() bool) *weeklyModule {
	return &weeklyModule{ws: ws, stack: stack, settings: settings, ready: ready}
}

// service resolves the SPEC-09 service of one project. The AI collaborators
// are optional: without a stack the returned service runs the documented
// skeleton path.
func (m *weeklyModule) service(ctx context.Context, projectID string) (weeklyreview.Service, error) {
	handle, err := m.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	opts := weeklyreview.DepsFromWorkspace(m.ws, handle)
	opts.Settings = m.settings
	opts.Logger = appLogger{}
	if m.stack != nil {
		readOnly := output.NewProjectReadOnly(m.ws.Global().DB(), projectID)
		outputs, oerr := output.NewOutputService(handle.DB(), output.OutputOptions{
			Clock:     m.ws.Clock(),
			ProjectID: projectID,
			ReadOnly:  readOnly,
		})
		if oerr != nil {
			appLogger{}.Warn("boot: 周报 AI 输出服务不可用", "err", oerr)
		} else {
			gen, gerr := output.NewGenerationService(output.GenerationOptions{
				Outputs:  outputs,
				Runner:   m.stack.Gateway,
				Events:   handle.Events(),
				ReadOnly: readOnly,
				Logger:   func(msg string, kv ...any) { appLogger{}.Info(msg, kv...) },
			})
			if gerr != nil {
				appLogger{}.Warn("boot: 周报 AI 生成服务不可用", "err", gerr)
			} else {
				opts.AI = gen
				opts.Outputs = outputs
			}
		}
	}
	return weeklyreview.New(m.ws, projectID, opts), nil
}

func (m *weeklyModule) DefaultPeriod(ctx context.Context, projectID string) (weeklyreview.Period, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return weeklyreview.Period{}, err
	}
	return svc.DefaultPeriod(ctx)
}

func (m *weeklyModule) List(ctx context.Context, projectID string, in weeklyreview.ListReportsInput) (weeklyreview.ReportPage, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return weeklyreview.ReportPage{}, err
	}
	return svc.ListReports(ctx, in)
}

func (m *weeklyModule) Get(ctx context.Context, projectID, reportID string) (*weeklyreview.WeeklyReport, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.GetReport(ctx, reportID)
}

func (m *weeklyModule) CollectActivities(ctx context.Context, projectID string, in weeklyreview.CollectInput) (*weeklyreview.ActivityDigest, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.CollectActivities(ctx, in)
}

func (m *weeklyModule) GenerateForPeriod(ctx context.Context, projectID string, in weeklyreview.GeneratePeriodInput) (*weeklyreview.GenerateOutcome, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.GenerateForPeriod(ctx, in)
}

func (m *weeklyModule) GenerateDraft(ctx context.Context, projectID string, in weeklyreview.GenerateInput) (*weeklyreview.GenerateOutcome, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.GenerateDraft(ctx, in)
}

func (m *weeklyModule) RegenerateSection(ctx context.Context, projectID string, in weeklyreview.SectionRevisionInput) (*weeklyreview.SectionRevisionOutcome, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.RegenerateSection(ctx, in)
}

func (m *weeklyModule) UpdateContent(ctx context.Context, projectID string, in weeklyreview.UpdateContentInput) (*weeklyreview.WeeklyReport, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.UpdateContent(ctx, in)
}

func (m *weeklyModule) Confirm(ctx context.Context, projectID string, in weeklyreview.ConfirmInput) (*weeklyreview.WeeklyReport, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.Confirm(ctx, in)
}

func (m *weeklyModule) RevertToDraft(ctx context.Context, projectID string, in weeklyreview.RevertInput) (*weeklyreview.WeeklyReport, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return svc.RevertToDraft(ctx, in)
}

func (m *weeklyModule) DeleteReport(ctx context.Context, projectID, reportID string) error {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return err
	}
	return svc.DeleteReport(ctx, reportID, weeklyreview.ActorUser)
}

func (m *weeklyModule) RestoreReport(ctx context.Context, projectID, reportID string) error {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return err
	}
	return svc.RestoreReport(ctx, reportID, weeklyreview.ActorUser)
}

func (m *weeklyModule) Export(ctx context.Context, projectID string, in weeklyreview.ExportInput) (*weeklyreview.ExportResult, error) {
	svc, err := m.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if in.Actor == "" {
		in.Actor = weeklyreview.ActorUser
	}
	return svc.Export(ctx, in)
}

// The skill set is workspace-level: it lives in the workspace settings store
// and needs no project handle, so the same calls serve Settings with no
// project open and the weekly dialog with one. projectID is part of the shared
// interface only; it is ignored here.

// skillService resolves the workspace-level service for skill CRUD. The
// project-scoped collaborators are left nil: no skill method reads them.
func (m *weeklyModule) skillService() weeklyreview.Service {
	return weeklyreview.New(m.ws, "", weeklyreview.Options{
		Clock:    m.ws.Clock(),
		Settings: m.settings,
		Logger:   appLogger{},
	})
}

func (m *weeklyModule) Skills(ctx context.Context, projectID string) (weeklyreview.SkillSet, error) {
	return m.skillService().Skills(ctx)
}

func (m *weeklyModule) SaveSkill(ctx context.Context, projectID string, in weeklyreview.SkillInput) (weeklyreview.SkillSet, error) {
	return m.skillService().SaveSkill(ctx, in)
}

func (m *weeklyModule) DeleteSkill(ctx context.Context, projectID, skillID string) (weeklyreview.SkillSet, error) {
	return m.skillService().DeleteSkill(ctx, skillID)
}

func (m *weeklyModule) SetDefaultSkill(ctx context.Context, projectID, skillID string) (weeklyreview.SkillSet, error) {
	return m.skillService().SetDefaultSkill(ctx, skillID)
}

// AIEnabled reports whether a chat provider is assigned, so the generation
// dialog names the deterministic skeleton and points at Settings instead of
// letting the model stage fail.
func (m *weeklyModule) AIEnabled(projectID string) bool {
	return m != nil && m.stack != nil && m.stack.Gateway != nil && m.ready != nil && m.ready()
}

// RevealFolder opens dir with the OS file manager (SPEC-09 §6.4). It is the
// body of shell.revealFolder, which is package-private; the page reaches it
// through this adapter instead of importing the shell.
func (m *weeklyModule) RevealFolder(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("weekly review: %s 不是文件夹", dir)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

// OpenFile opens one file with the OS default application. It is the target of
// the export toast's link: the page hands over the path its own export wrote,
// never one typed by the user. As with RevealFolder the command is only
// started: the viewer outlives the click and the call returns at once.
func (m *weeklyModule) OpenFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("weekly review: %s 不是文件", path)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}
