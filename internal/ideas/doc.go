// Package ideas implements SPEC-06: the per-project Idea lifecycle, the
// Evidence entity (manual and AI-drafted), Idea relations and the Activity
// driven Idea History.
//
// Persistence follows the frozen SPEC-01 schema rather than the logical column
// names of SPEC-06:
//
//   - ideas.description            ← SPEC-06 description_md
//   - evidence.content             ← SPEC-06 content_md
//   - evidence.provenance          ← SPEC-06 provenance (JSON column, SPEC-01 §4.4 A2)
//   - evidence.content_hash        ← added by this module's 0200-band migration
//   - idea status values           ← PascalCase, as frozen in SPEC-00 §4 / SPEC-01
//
// Every mutating method runs in one SPEC-01 UnitOfWork (workspace.ProjectHandle
// .InTx): the business write, the relation write and the Activity event commit
// atomically. Actor=ai is rejected for all write entry points; only the AI
// skill's own ai.analysis_completed event carries actor=ai.
//
// Error-code additions beyond SPEC-06 §5.9 (all additive, documented here):
//
//	input.invalid                 field validation failure
//	project.not_found             the bound project row is missing
//	relation.target_not_found     relation target does not exist
//	relation.target_deleted       relation target is soft-deleted
//	evidence.not_ai_draft         ConfirmEvidence received manual evidence
package ideas
