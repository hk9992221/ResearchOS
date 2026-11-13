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
//   - weekly_reports.ai_output_id points at the SPEC-02 draft that was used;
//     confirmed_at is written on confirm
//
// No report body file exists; export writes a standalone Markdown document.
// The report title is derived from the project name plus period because the
// frozen schema has no title column (SPEC-09 §4.2 A).
//
// Two deliberate adaptations:
//
//   - events.Query.Until is inclusive in SPEC-01, so the digest passes
//     EndExclusive-1ns to obtain the required [Start, EndExclusive) window.
//     Every stored timestamp has millisecond precision, so the bound is exact.
//   - ActivityDigest keeps Blocked/Planned as dedicated fields (they are
//     rendered in sections 6 and 7 by BuildSkeleton) instead of injecting
//     synthetic State=current/planned items into Sections; the event counts
//     therefore stay clean (SPEC-09 §5.3 step 5).
//
// All writes run through workspace.ProjectHandle.InTx so the business write
// and the Activity event commit atomically. Events go through
// foundation/events only. AI generation goes through SPEC-02's GenerationService
// (which owns ai_outputs and ai.analysis_completed); this package never emits
// ai.analysis_completed itself.
//
// The UI and the SPEC-11 MCP server call the same Service methods.
package weeklyreview
