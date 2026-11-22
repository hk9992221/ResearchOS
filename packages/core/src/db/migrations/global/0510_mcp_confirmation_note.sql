-- ============================================================
-- Rewrite addition (0510, global.db) — MCP confirmation approval note.
-- SPEC-11 §11.3: important judgments keep a change explanation. The desktop
-- approval path stores the operator's note on the row; MCP clients never set it.
-- Version 0510 is free above the rewrite band's 0500 and has no Go counterpart;
-- existing rows receive the empty-string default without being rewritten.
-- ============================================================

ALTER TABLE mcp_confirmation ADD COLUMN note TEXT NOT NULL DEFAULT '';