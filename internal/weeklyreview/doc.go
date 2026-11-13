// Package weeklyreview implements SPEC-09: the per-project weekly report
// pipeline. It turns the project Activity stream into a deterministic digest,
// renders a seven-section Markdown report (manually or with AI assistance),
// validates AI drafts against the spec's hard constraints, and manages the
// draft → confirmed lifecycle plus Markdown export.
//
// Persistence uses the frozen SPEC-01 project schema:
//
//   - weekly_reports.content_md  Markdown body, the only content format
//   - weekly_reports.meta        JSON provenance / coverage / check state
//   - weekly_reports.confirmed_at and ai_output_id are written on confirm
//
// No report body file exists; export writes a standalone Markdown document.
//
// All writes run through workspace.ProjectHandle.InTx so the business write
// and the Activity event commit atomically. Events go through
// foundation/events only. AI generation goes through SPEC-02's GenerationService
// (which owns ai_outputs and ai.analysis_completed); this package never emits
// ai.analysis_completed itself.
//
// The UI and the SPEC-11 MCP server call the same Service methods.
package weeklyreview
