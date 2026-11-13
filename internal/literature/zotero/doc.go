// Package zotero implements the read-only Zotero side of SPEC-04:
// connection detection (LIT-P1), metadata readers (LIT-P2) and the fallback
// source that prefers Zotero's local API and falls back to a read-only SQLite
// connection.
//
// Everything here is non-invasive. Detection only stats paths and reads
// prefs.js; validation and metadata reads open zotero.sqlite with mode=ro and
// query_only(1). Nothing in the Zotero data directory is created, moved,
// modified or deleted, and no background work is started.
//
// Open item O-19: modernc.org/sqlite with mode=ro may still create a -shm file
// next to a WAL-mode database if SQLite decides it needs one. The P0 spike must
// confirm this on real Zotero 7 data. If side files appear, switch to a
// snapshot copy or the SQLite backup API; this package deliberately does not
// copy or mutate the live database on its own.
package zotero
