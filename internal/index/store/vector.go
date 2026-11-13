package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"researchos/internal/foundation/clock"
)

// vector is the SQLite VectorIndex: brute-force cosine over float32 blobs,
// isolated per EmbeddingProfileID (O-03 spike decision: v1 exact search, the
// interface hides future ANN choices).
type vector struct {
	db        *sql.DB
	clock     clock.Clock
	tb        tables
	partition Partition
}

var _ VectorIndex = (*vector)(nil)

// NewVectorIndex returns the vector primitive for one partition database.
func NewVectorIndex(db *sql.DB, partition Partition, c clock.Clock) (VectorIndex, error) {
	if db == nil {
		return nil, errors.New("index/store: nil database")
	}
	return &vector{db: db, clock: clockOrDefault(c), tb: partition.tables(), partition: partition}, nil
}

// ReplaceDoc validates the batch completely BEFORE writing (dimension and
// chunk coverage), so a successful call always stores every vector or none
// (§5.10.3 rules 1 and 3).
func (v *vector) ReplaceDoc(ctx context.Context, profileID string, doc IndexDoc, chunks []IndexChunk, vectors []ChunkVector) error {
	if profileID == "" {
		return ErrEmptyProfile
	}
	if err := validateReplace(doc, chunks); err != nil {
		return err
	}
	blobDim, payloads, err := encodeBatch(vectors)
	if err != nil {
		return err
	}
	if len(vectors) != len(chunks) {
		return ErrVectorCount
	}
	chunkIDs := make(map[string]bool, len(chunks))
	for _, ch := range chunks {
		chunkIDs[ch.ChunkID] = true
	}
	byChunk := make(map[string]int, len(vectors))
	for i, cv := range vectors {
		if !chunkIDs[cv.ChunkID] {
			return ErrVectorCount
		}
		if _, dup := byChunk[cv.ChunkID]; dup {
			return ErrDuplicateChunkID
		}
		byChunk[cv.ChunkID] = i
	}
	if len(chunks) > 0 {
		storedDim, ok, err := v.profileDim(ctx, profileID)
		if err != nil {
			return err
		}
		if ok && storedDim != blobDim {
			return fmt.Errorf("%w: profile has dim %d, batch has dim %d", ErrVectorDim, storedDim, blobDim)
		}
	}

	return v.inTx(ctx, func(tx *sql.Tx) error {
		if err := v.ensureStored(ctx, tx, doc, chunks); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE profile_id = ? AND doc_id = ?`,
				v.tb.vectors), profileID, doc.DocID); err != nil {
			return fmt.Errorf("index/store: delete profile vectors: %w", err)
		}
		for _, ch := range chunks {
			i := byChunk[ch.ChunkID]
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`INSERT INTO %s (profile_id, doc_id, chunk_id, dim, vector) VALUES (?, ?, ?, ?, ?)`,
					v.tb.vectors), profileID, doc.DocID, ch.ChunkID, blobDim, payloads[i]); err != nil {
				return fmt.Errorf("index/store: insert vector %s: %w", ch.ChunkID, err)
			}
		}
		return nil
	})
}

// DeleteDoc removes every profile's vectors for the document; idempotent
// and independent of whether the keyword chunks are still present.
func (v *vector) DeleteDoc(ctx context.Context, projectID, docID string) error {
	if docID == "" {
		return ErrEmptyDocID
	}
	_, err := v.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE doc_id = ?`, v.tb.vectors), docID)
	if err != nil {
		return fmt.Errorf("index/store: delete doc vectors: %w", err)
	}
	return nil
}

// Query scans exactly one profile and ranks by cosine similarity.
func (v *vector) Query(ctx context.Context, profileID string, q []float32, topK int, filter IndexFilter) ([]ChunkHit, error) {
	if profileID == "" {
		return nil, ErrEmptyProfile
	}
	if topK <= 0 {
		topK = 50
	}
	if len(q) == 0 {
		return nil, nil
	}
	sqlQ := fmt.Sprintf(
		`SELECT c.chunk_id, c.doc_id, %s, c.text, c.locator_json, v.vector
		 FROM %s v
		 JOIN %s c ON c.chunk_id = v.chunk_id
		 JOIN %s d ON d.doc_id = c.doc_id
		 WHERE v.profile_id = ?%s`,
		v.projectSel(), v.tb.vectors, v.tb.chunks, v.tb.docs, filterCond(filter))
	args := []any{profileID}
	args = append(args, filterArgs(filter)...)
	rows, err := v.db.QueryContext(ctx, sqlQ, args...)
	if err != nil {
		return nil, fmt.Errorf("index/store: vector query: %w", err)
	}
	defer rows.Close()

	type scored struct {
		hit ChunkHit
		sim float64
	}
	var out []scored
	for rows.Next() {
		var (
			h       ChunkHit
			locator string
			blob    []byte
		)
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.ProjectID, &h.Text, &locator, &blob); err != nil {
			return nil, fmt.Errorf("index/store: vector scan: %w", err)
		}
		vec, err := decodeVector(blob)
		if err != nil {
			return nil, fmt.Errorf("index/store: vector decode %s: %w", h.ChunkID, err)
		}
		sim, ok := cosine(q, vec)
		if !ok {
			// A stored dimension that no longer matches the profile's batch
			// shape is skipped rather than mixed into results (§5.10.3 rule 3).
			continue
		}
		if locator != "" {
			h.LocatorJSON = json.RawMessage(locator)
		}
		out = append(out, scored{hit: h, sim: sim})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].sim != out[j].sim {
			return out[i].sim > out[j].sim
		}
		if out[i].hit.DocID != out[j].hit.DocID {
			return out[i].hit.DocID < out[j].hit.DocID
		}
		return out[i].hit.ChunkID < out[j].hit.ChunkID
	})
	if len(out) > topK {
		out = out[:topK]
	}
	hits := make([]ChunkHit, len(out))
	for i, s := range out {
		s.hit.Score = s.sim
		hits[i] = s.hit
	}
	return hits, nil
}

func (v *vector) projectSel() string {
	if v.tb.hasProject {
		return "d.project_id"
	}
	return "''"
}

// ensureStored verifies the document and exactly the provided chunk rows
// already live in the keyword-owned tables. Vector rows deliberately carry
// no cascade FK: keyword ReplaceDoc/DeleteDoc must never silently destroy
// other profiles' vectors, so stale rows become invisible through the join
// and are removed only through the two DeleteDoc calls (SPEC-10 rule 2).
func (v *vector) ensureStored(ctx context.Context, tx *sql.Tx, doc IndexDoc, chunks []IndexChunk) error {
	var exists int
	var err error
	if v.tb.hasProject {
		err = tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT 1 FROM %s WHERE doc_id = ? AND project_id = ?`, v.tb.docs),
			doc.DocID, doc.ProjectID).Scan(&exists)
	} else {
		err = tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT 1 FROM %s WHERE doc_id = ?`, v.tb.docs), doc.DocID).Scan(&exists)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("index/store: document %s is not keyword-indexed yet; run KeywordIndex.ReplaceDoc first", doc.DocID)
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT chunk_id FROM %s WHERE doc_id = ?`, v.tb.chunks), doc.DocID)
	if err != nil {
		return fmt.Errorf("index/store: stored chunk ids: %w", err)
	}
	defer rows.Close()
	stored := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		stored[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ch := range chunks {
		if !stored[ch.ChunkID] {
			return fmt.Errorf("index/store: chunk %s is not stored for document %s; run KeywordIndex.ReplaceDoc first", ch.ChunkID, doc.DocID)
		}
		delete(stored, ch.ChunkID)
	}
	if len(stored) > 0 {
		return fmt.Errorf("index/store: vectors cover only part of document %s chunks", doc.DocID)
	}
	return nil
}

func (v *vector) profileDim(ctx context.Context, profileID string) (int, bool, error) {
	var dim int
	err := v.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT dim FROM %s WHERE profile_id = ? LIMIT 1`, v.tb.vectors), profileID).Scan(&dim)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("index/store: profile dim: %w", err)
	}
	return dim, true, nil
}

func (v *vector) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("index/store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// encodeBatch validates uniform dimensions and returns the shared dim plus
// one little-endian float32 blob per vector.
func encodeBatch(vectors []ChunkVector) (int, [][]byte, error) {
	dim := -1
	payloads := make([][]byte, len(vectors))
	for i, cv := range vectors {
		if len(cv.Vector) == 0 {
			return 0, nil, ErrVectorDim
		}
		if dim < 0 {
			dim = len(cv.Vector)
		} else if len(cv.Vector) != dim {
			return 0, nil, ErrVectorDim
		}
		buf := make([]byte, 4*dim)
		for j, f := range cv.Vector {
			binary.LittleEndian.PutUint32(buf[4*j:], math.Float32bits(f))
		}
		payloads[i] = buf
	}
	if dim < 0 {
		dim = 0
	}
	return dim, payloads, nil
}

func decodeVector(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 || len(blob) == 0 {
		return nil, ErrVectorDim
	}
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[4*i:]))
	}
	return out, nil
}

func cosine(a, b []float32) (float64, bool) {
	if len(a) != len(b) {
		return 0, false
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0, false
	}
	return dot / math.Sqrt(na*nb), true
}
