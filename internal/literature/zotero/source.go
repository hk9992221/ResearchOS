package zotero

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"researchos/internal/literature"
)

var _ literature.ZoteroSource = (*Source)(nil)

// Source implements literature.ZoteroSource: it prefers the local API and
// falls back to the read-only SQLite reader. A fallback is decided per call,
// never cached forever: every Reader call re-probes the API when a Connection
// is available, and a failing API call inside a returned reader is retried once
// against SQLite (the whole call, so a page is never half API and half SQLite).
type Source struct {
	conn   *Connection
	sqlite *SQLiteReader
	api    literature.ZoteroReader

	mu         sync.Mutex
	lastReason string
}

// NewSource wires the three collaborators. conn and api may be nil; sqlite may
// be nil when only the local API is configured. When both readers exist and the
// API reader is a *LocalAPIReader, the SQLite attachment resolver is shared so
// storage: paths work from either source.
func NewSource(conn *Connection, sqlite *SQLiteReader, api literature.ZoteroReader) *Source {
	s := &Source{conn: conn, sqlite: sqlite, api: api}
	if sqlite != nil && sqlite.LinkedBaseDir == "" && conn != nil {
		sqlite.LinkedBaseDir = conn.LinkedAttachmentBaseDir()
	}
	if lr, ok := api.(*LocalAPIReader); ok && sqlite != nil && lr.resolver.DataDir == "" {
		lr.WithResolver(sqlite.resolver())
	}
	return s
}

// Reader returns the reader for this call.
func (s *Source) Reader(ctx context.Context) (literature.ZoteroReader, error) {
	ready, reason := s.apiReady(ctx)
	if ready {
		var fallback literature.ZoteroReader
		if s.sqlite != nil {
			fallback = s.sqlite
		}
		return &fallbackReader{source: s, primary: s.api, fallback: fallback}, nil
	}
	if s.sqlite != nil {
		s.recordFallback(fmt.Errorf("%s", reason))
		return s.sqlite, nil
	}
	if reason != "" {
		// The API is the only way to read a live library; naming why it is
		// unusable turns "均不可用" into an actionable hint (§5.1 diagnostics).
		s.recordFallback(fmt.Errorf("%s", reason))
		return nil, fmt.Errorf("%w：%s", literature.ErrZoteroUnavailable, reason)
	}
	return nil, literature.ErrZoteroUnavailable
}

// Status reports the currently preferred source and the last fallback reason.
func (s *Source) Status(ctx context.Context) (literature.MetadataSource, string, error) {
	last := s.reason()

	if s.conn != nil {
		probe := s.conn.probeLocalAPI(ctx)
		if probe.Available {
			if s.api == nil {
				if s.sqlite != nil {
					return literature.SourceSQLite, last, nil
				}
				return "", last, literature.ErrZoteroUnavailable
			}
			return literature.SourceLocalAPI, last, nil
		}
		if s.sqlite != nil {
			reason := probe.Error
			if last != "" {
				reason = last
			} else {
				s.recordFallback(fmt.Errorf("%s", reason))
			}
			return literature.SourceSQLite, reason, nil
		}
		return "", probe.Error, literature.ErrZoteroUnavailable
	}

	if s.api == nil {
		if s.sqlite != nil {
			return literature.SourceSQLite, nonEmpty(last, "本地 API 未配置"), nil
		}
		return "", nonEmpty(last, "本地 API 未配置"), literature.ErrZoteroUnavailable
	}
	if last != "" {
		if s.sqlite != nil {
			return literature.SourceSQLite, last, nil
		}
		return "", last, literature.ErrZoteroUnavailable
	}
	return literature.SourceLocalAPI, "", nil
}

func (s *Source) apiReady(ctx context.Context) (bool, string) {
	if s.api == nil {
		return false, "本地 API 未配置"
	}
	if s.conn == nil {
		return true, ""
	}
	status := s.conn.probeLocalAPI(ctx)
	if status.Available {
		return true, ""
	}
	return false, nonEmpty(status.Error, hintZoteroClosed)
}

func (s *Source) recordFallback(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastReason = err.Error()
	s.mu.Unlock()
}

func (s *Source) recordSuccess() {
	s.mu.Lock()
	s.lastReason = ""
	s.mu.Unlock()
}

func (s *Source) reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReason
}

func nonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// fallbackReader runs one call against the API and, if it fails, retries the
// entire call against SQLite and records why.
type fallbackReader struct {
	source   *Source
	primary  literature.ZoteroReader
	fallback literature.ZoteroReader
}

func (f *fallbackReader) Source() literature.MetadataSource { return f.primary.Source() }

func (f *fallbackReader) useFallback(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() == nil && f.fallback != nil
}

// noteFailure records a failure that indicates the API source is unusable.
// Ordinary "not found" answers are not source failures.
func (f *fallbackReader) noteFailure(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	if !isSourceFailure(err) {
		f.source.recordSuccess()
		return
	}
	if f.fallback == nil {
		f.source.recordFallback(err)
	}
}

func isSourceFailure(err error) bool {
	if err == nil {
		return false
	}
	for _, benign := range []error{
		literature.ErrItemNotFound,
		literature.ErrAttachmentUnresolved,
		literature.ErrPathOutsideStorage,
		literature.ErrNoPDFAttachment,
		literature.ErrAttachmentChoice,
	} {
		if errors.Is(err, benign) {
			return false
		}
	}
	return true
}

func (f *fallbackReader) ListLibraries(ctx context.Context) ([]literature.ZoteroLibrary, error) {
	out, err := f.primary.ListLibraries(ctx)
	if err == nil {
		f.source.recordSuccess()
		return out, nil
	}
	if !f.useFallback(ctx, err) {
		f.noteFailure(ctx, err)
		return out, err
	}
	f.source.recordFallback(err)
	return f.fallback.ListLibraries(ctx)
}

func (f *fallbackReader) ListCollections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error) {
	out, err := f.primary.ListCollections(ctx, libraryID)
	if err == nil {
		f.source.recordSuccess()
		return out, nil
	}
	if !f.useFallback(ctx, err) {
		f.noteFailure(ctx, err)
		return out, err
	}
	f.source.recordFallback(err)
	return f.fallback.ListCollections(ctx, libraryID)
}

func (f *fallbackReader) ListItems(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	out, err := f.primary.ListItems(ctx, libraryID, collectionKey, opts)
	if err == nil {
		f.source.recordSuccess()
		if out.SourceUsed == "" {
			out.SourceUsed = f.primary.Source()
		}
		return out, nil
	}
	if !f.useFallback(ctx, err) {
		f.noteFailure(ctx, err)
		return out, err
	}
	f.source.recordFallback(err)
	return f.fallback.ListItems(ctx, libraryID, collectionKey, opts)
}

func (f *fallbackReader) GetItem(ctx context.Context, libraryID, key string) (literature.ZoteroItem, error) {
	out, err := f.primary.GetItem(ctx, libraryID, key)
	if err == nil {
		f.source.recordSuccess()
		return out, nil
	}
	if !f.useFallback(ctx, err) {
		f.noteFailure(ctx, err)
		return out, err
	}
	f.source.recordFallback(err)
	return f.fallback.GetItem(ctx, libraryID, key)
}

func (f *fallbackReader) ListAttachments(ctx context.Context, libraryID, parentKey string) ([]literature.ZoteroAttachment, error) {
	out, err := f.primary.ListAttachments(ctx, libraryID, parentKey)
	if err == nil {
		f.source.recordSuccess()
		return out, nil
	}
	if !f.useFallback(ctx, err) {
		f.noteFailure(ctx, err)
		return out, err
	}
	f.source.recordFallback(err)
	return f.fallback.ListAttachments(ctx, libraryID, parentKey)
}

func (f *fallbackReader) ResolveAttachment(ctx context.Context, att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	out, err := f.primary.ResolveAttachment(ctx, att)
	if err == nil {
		f.source.recordSuccess()
		return out, nil
	}
	if !f.useFallback(ctx, err) {
		f.noteFailure(ctx, err)
		return out, err
	}
	f.source.recordFallback(err)
	return f.fallback.ResolveAttachment(ctx, att)
}
