// Package store implements the keyword and vector index storage primitives
// consumed by SPEC-10 (SPEC-02 §5.10). Table shapes follow the SPEC-10 §4.2
// schema adopted by SPEC-00 v1.1 (N1/N2/N3): source registries, dirty
// queues, chunking, extraction and job orchestration are NOT here — this
// package only writes, replaces, deletes and queries docs/chunks/vectors.
//
// LocatorJSON is opaque pass-through data; nothing in this package parses it
// (AC-08). Vector spaces are isolated by EmbeddingProfileID (AC-10).
package store

import (
	"context"
	"encoding/json"
	"errors"

	"researchos/internal/foundation/clock"
)

// Validation sentinels shared by both indexes.
var (
	ErrEmptyDocID       = errors.New("index/store: doc id must not be empty")
	ErrChunkDocMismatch = errors.New("index/store: chunk does not belong to the document")
	ErrDuplicateChunkID = errors.New("index/store: duplicate chunk id in ReplaceDoc")
	ErrVectorCount      = errors.New("index/store: vectors must cover exactly the document chunks")
	ErrVectorDim        = errors.New("index/store: vector dimensions are inconsistent")
	ErrEmptyProfile     = errors.New("index/store: profile id must not be empty")
)

// IndexDoc mirrors the SPEC-10 document projection; ProjectID is empty for
// the global literature partition.
type IndexDoc struct {
	ProjectID   string
	DocID       string
	Source      string // paper_pdf / note / db_record / idea / experiment
	EntityType  string
	EntityID    string
	Title       string
	ContentHash string
}

// IndexChunk is one stored retrieval unit. Ordinal keeps per-doc order
// stable; LocatorJSON is never interpreted here.
type IndexChunk struct {
	ChunkID     string
	DocID       string
	ProjectID   string
	Ordinal     int
	Text        string
	ContentHash string
	LocatorJSON json.RawMessage
}

// ChunkVector pairs a chunk with its embedding under one profile.
type ChunkVector struct {
	ChunkID string
	Vector  []float32
}

// ChunkHit is one retrieval result; Score is higher-is-better for both
// indexes (keyword: -bm25; vector: cosine).
type ChunkHit struct {
	ChunkID      string
	DocID        string
	ProjectID    string
	Text         string
	Score        float64
	MatchedTerms []string
	LocatorJSON  json.RawMessage
}

// IndexFilter restricts or excludes documents before top-k truncation.
type IndexFilter struct {
	DocIDs        []string
	ExcludeDocIDs []string
}

// KeywordIndex is the keyword storage primitive (SPEC-02 §5.10.3).
type KeywordIndex interface {
	// ReplaceDoc atomically replaces all chunks of one document inside its
	// partition; repeated calls with the same input are idempotent.
	ReplaceDoc(ctx context.Context, doc IndexDoc, chunks []IndexChunk) error
	// DeleteDoc removes the document's keyword index; absent → nil.
	DeleteDoc(ctx context.Context, projectID, docID string) error
	// Query returns hits for this partition only, honoring the filter.
	Query(ctx context.Context, projectID, text string, topK int, filter IndexFilter) ([]ChunkHit, error)
}

// VectorIndex is the profile-isolated vector storage primitive.
type VectorIndex interface {
	// ReplaceDoc writes the vectors of one document under profileID. Other
	// profiles of the same document are untouched. Either every vector is
	// visible afterwards or none; half batches are rejected (§5.10.3 rule 1).
	ReplaceDoc(ctx context.Context, profileID string, doc IndexDoc, chunks []IndexChunk, vectors []ChunkVector) error
	// DeleteDoc removes the document's vectors across ALL profiles.
	DeleteDoc(ctx context.Context, projectID, docID string) error
	// Query searches one profile only; cross-profile mixing is impossible.
	Query(ctx context.Context, profileID string, vector []float32, topK int, filter IndexFilter) ([]ChunkHit, error)
}

// Partition selects the table set: the per-project index inside a
// project.db, or the global literature index inside global.db (N1).
type Partition int

// Supported partitions.
const (
	// PartitionProject uses index_docs / index_chunks / index_chunks_fts /
	// chunk_embeddings in one project.db.
	PartitionProject Partition = iota
	// PartitionGlobal uses paper_index_docs / paper_chunks / paper_fts /
	// paper_chunk_vectors in global.db.
	PartitionGlobal
)

type tables struct {
	docs       string
	chunks     string
	fts        string
	vectors    string
	hasProject bool
}

func (p Partition) tables() tables {
	if p == PartitionGlobal {
		return tables{
			docs:    "paper_index_docs",
			chunks:  "paper_chunks",
			fts:     "paper_fts",
			vectors: "paper_chunk_vectors",
		}
	}
	return tables{
		docs:       "index_docs",
		chunks:     "index_chunks",
		fts:        "index_chunks_fts",
		vectors:    "chunk_embeddings",
		hasProject: true,
	}
}

// clockOrDefault normalizes a nil clock.
func clockOrDefault(c clock.Clock) clock.Clock {
	if c == nil {
		return clock.System{}
	}
	return c
}

// validateReplace checks the doc/chunk shape shared by both indexes.
func validateReplace(doc IndexDoc, chunks []IndexChunk) error {
	if doc.DocID == "" {
		return ErrEmptyDocID
	}
	seen := make(map[string]bool, len(chunks))
	for _, ch := range chunks {
		if ch.DocID != doc.DocID {
			return ErrChunkDocMismatch
		}
		if ch.ChunkID == "" || seen[ch.ChunkID] {
			return ErrDuplicateChunkID
		}
		seen[ch.ChunkID] = true
	}
	return nil
}
