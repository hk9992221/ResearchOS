-- ============================================================
-- Rewrite addition (0500, project.db) — PDF annotations.
-- SPEC-04 extension: page-precise highlights/notes over an imported PDF copy.
-- A row links a paper (and therefore a project) to a page, normalized rects, a
-- quote and optional Paper Note / Idea Evidence jump targets. It is not a note
-- body: no Markdown file is involved.
--
-- Version 0500 is the first free slot above the Go migration history (0400);
-- this file has no Go counterpart by design (rewrite additions band).
-- ============================================================

CREATE TABLE paper_annotations (
  id          TEXT    NOT NULL PRIMARY KEY,
  project_id  TEXT    NOT NULL,
  paper_id    TEXT    NOT NULL,
  note_id     TEXT    REFERENCES notes(id)    ON DELETE SET NULL,
  evidence_id TEXT    REFERENCES evidence(id) ON DELETE SET NULL,
  page_no     INTEGER NOT NULL CHECK (page_no > 0),
  rects       TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(rects)),
  quote       TEXT    NOT NULL DEFAULT '',
  color       TEXT,
  locator     TEXT    CHECK (locator IS NULL OR json_valid(locator)),
  created_at  TEXT    NOT NULL,
  updated_at  TEXT    NOT NULL
);

CREATE INDEX idx_paper_annotations_paper
  ON paper_annotations(paper_id);

CREATE INDEX idx_paper_annotations_project_paper
  ON paper_annotations(project_id, paper_id, page_no);

CREATE INDEX idx_paper_annotations_note
  ON paper_annotations(note_id)
  WHERE note_id IS NOT NULL;