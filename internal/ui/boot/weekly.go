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
}

var _ pages.WeeklyAPI = (*weeklyModule)(nil)

func newWeeklyModule(ws *fworkspace.Workspace, stack *wire.Stack, settings *settings.Store) *weeklyModule {
	return &weeklyModule{ws: ws, stack: stack, settings: settings}
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

// AIEnabled reports whether a generation stack is bound for this workspace.
func (m *weeklyModule) AIEnabled(projectID string) bool {
	return m.stack != nil && m.stack.Gateway != nil
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
