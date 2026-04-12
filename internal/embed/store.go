package embed

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store persists embeddings in SQLite with cosine similarity search.
type Store struct {
	db *sql.DB
}

// Embedding represents a stored vector with metadata.
type Embedding struct {
	ID          string            `json:"id"`
	Collection  string            `json:"collection"`
	ContentHash string            `json:"content_hash"`
	Vector      []float32         `json:"-"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   string            `json:"created_at"`
}

// SimilarResult pairs an embedding with its similarity score.
type SimilarResult struct {
	Embedding
	Score float64 `json:"score"`
}

// NewStore opens or creates an embedding store at the given DB path.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate embeddings: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS embeddings (
			id           TEXT PRIMARY KEY,
			collection   TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			vector       BLOB NOT NULL,
			metadata     TEXT,
			created_at   TEXT NOT NULL,
			UNIQUE(collection, content_hash)
		);
		CREATE INDEX IF NOT EXISTS idx_embeddings_collection
			ON embeddings(collection);
	`)
	return err
}

// Put stores an embedding, deduplicating by (collection, content_hash).
// Returns the ID (existing or new).
func (s *Store) Put(e Embedding) (string, error) {
	if e.ContentHash == "" {
		e.ContentHash = hashVector(e.Vector)
	}
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	metaJSON, _ := json.Marshal(e.Metadata)
	vecBlob := encodeVector(e.Vector)

	_, err := s.db.Exec(`
		INSERT INTO embeddings (id, collection, content_hash, vector, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection, content_hash) DO UPDATE SET
			vector = excluded.vector,
			metadata = excluded.metadata
	`, e.ID, e.Collection, e.ContentHash, vecBlob, string(metaJSON), e.CreatedAt)
	if err != nil {
		return "", err
	}

	// Return actual stored ID (may differ on conflict)
	var id string
	if err := s.db.QueryRow(`
		SELECT id FROM embeddings WHERE collection = ? AND content_hash = ?
	`, e.Collection, e.ContentHash).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// Similar returns the top-n most similar embeddings to the query vector
// within a collection.
func (s *Store) Similar(collection string, query []float32, n int) ([]SimilarResult, error) {
	rows, err := s.db.Query(`
		SELECT id, collection, content_hash, vector, metadata, created_at
		FROM embeddings
		WHERE collection = ?
	`, collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SimilarResult
	for rows.Next() {
		var (
			e       Embedding
			vecBlob []byte
			metaStr sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.Collection, &e.ContentHash, &vecBlob, &metaStr, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Vector = decodeVector(vecBlob)
		if metaStr.Valid && metaStr.String != "" {
			_ = json.Unmarshal([]byte(metaStr.String), &e.Metadata)
		}

		score := CosineSimilarity(query, e.Vector)
		results = append(results, SimilarResult{Embedding: e, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if n > 0 && len(results) > n {
		results = results[:n]
	}
	return results, rows.Err()
}

// ListCollections returns distinct collection names.
func (s *Store) ListCollections() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT collection FROM embeddings ORDER BY collection`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// DeleteCollection removes all embeddings in the given collection.
func (s *Store) DeleteCollection(collection string) error {
	_, err := s.db.Exec(`DELETE FROM embeddings WHERE collection = ?`, collection)
	return err
}

// CollectionCount returns the number of embeddings in a collection.
func (s *Store) CollectionCount(collection string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE collection = ?`, collection).Scan(&count)
	return count, err
}

// --- helpers ---

func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeVector(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func hashVector(v []float32) string {
	h := sha256.New()
	for _, f := range v {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], math.Float32bits(f))
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
