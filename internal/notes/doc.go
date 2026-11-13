// Package notes is the SPEC-05 Research Notes backend service layer: the
// unified note store (Markdown file + project.db metadata), research databases,
// records, tags, relations, attachments and file reconciliation.
//
// It is deliberately UI- and MCP-agnostic: the desktop UI and the MCP server
// call exactly these services, so validation and Activity events stay in one
// place (SPEC-00 §12). Bodies are Markdown files under
// projects/<project-id>/notes/; project.db is the metadata authority
// (SPEC-00 §1). All writes run through one transaction, events go through
// foundation/events only, and note.updated is emitted exclusively by
// notefile/reconcile so it is merged per note per day (SPEC-01 §5.6.4).
package notes
