package cortex

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/tenancy"
)

// Test helpers shared by more than one root test file.

func ids(ms []Memory) string {
	s := make([]string, len(ms))
	for i, m := range ms {
		s[i] = m.ID
	}
	sort.Strings(s)
	out := ""
	for _, id := range s {
		out += id + ","
	}
	return out
}

// mockExtractor is a test-only extractor mock.
type mockExtractor struct {
	extractFn func(ctx context.Context, content, contentType string) (*Extraction, error)
}

func (m *mockExtractor) Extract(ctx context.Context, content, contentType string) (*Extraction, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx, content, contentType)
	}
	return &Extraction{}, nil
}

func openTestDB(t *testing.T) *Cortex {
	t.Helper()
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return modeHandle(t, c)
}

// ownerCortex returns a handle scoped to a fresh user whose owner entity is
// named name.
func ownerCortex(t *testing.T, name, email string) (*Cortex, string) {
	t.Helper()
	root, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	u, err := root.CreateUser(context.Background(), "", email, name, true)
	if err != nil {
		t.Fatal(err)
	}
	c, uk := keyedHandleUK(t, root, u.ID)
	sealOwner(t, root, uk, u.ID, name)
	ownerID, err := tenancy.New(c.h).OwnerEntityID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c, ownerID
}

// twoUsers returns handles for two fresh users sharing one database.
func twoUsers(t *testing.T) (a, b *Cortex) {
	t.Helper()
	root, err := Open(pgtest.DSN(t), WithEmbedder(&testEmbedder{}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	ctx := context.Background()
	ua, err := root.CreateUser(ctx, "sub-a", "a@example.com", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	ub, err := root.CreateUser(ctx, "sub-b", "b@example.com", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	return keyedHandle(t, root, ua.ID), keyedHandle(t, root, ub.ID)
}

// markOwner flags an entity as the owner (fixture helper).
func markOwner(t *testing.T, c *Cortex, id string) {
	t.Helper()
	if _, err := c.h.DB.ExecContext(context.Background(), `UPDATE entities SET is_owner = TRUE WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

// mockLLM is a test-only LLM mock.
type mockLLM struct {
	extractFn         func(ctx context.Context, text, prompt string) (ExtractionResult, error)
	decomposeFn       func(ctx context.Context, query string) ([]StructuredQuery, error)
	summarizeFn       func(ctx context.Context, texts []string) (string, error)
	detectConflictsFn func(ctx context.Context, memories []Memory) ([]ConflictPair, error)
	detectRelationsFn func(ctx context.Context, memories []Memory) ([]MemoryRelation, error)
}

func (m *mockLLM) Extract(ctx context.Context, text, prompt string) (ExtractionResult, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx, text, prompt)
	}
	return ExtractionResult{}, nil
}

func (m *mockLLM) Decompose(ctx context.Context, query string) ([]StructuredQuery, error) {
	if m.decomposeFn != nil {
		return m.decomposeFn(ctx, query)
	}
	return nil, nil
}

func (m *mockLLM) Summarize(ctx context.Context, texts []string) (string, error) {
	if m.summarizeFn != nil {
		return m.summarizeFn(ctx, texts)
	}
	return "", nil
}

func (m *mockLLM) DetectConflicts(ctx context.Context, memories []Memory) ([]ConflictPair, error) {
	if m.detectConflictsFn != nil {
		return m.detectConflictsFn(ctx, memories)
	}
	return nil, nil
}

func (m *mockLLM) DetectRelations(ctx context.Context, memories []Memory) ([]MemoryRelation, error) {
	if m.detectRelationsFn != nil {
		return m.detectRelationsFn(ctx, memories)
	}
	return nil, nil
}

func fastBackoff(t *testing.T) {
	t.Helper()
	old := llmutil.Backoff
	llmutil.Backoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { llmutil.Backoff = old })
}

func openTestDBWithEmbedder(t *testing.T) *Cortex {
	t.Helper()
	embedder := &testEmbedder{}
	c, err := Open(pgtest.DSN(t), WithEmbedder(embedder))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return modeHandle(t, c)
}

// testEmbedder produces deterministic 8-dim normalized vectors from text bytes.
type testEmbedder struct{}

func (e *testEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		result[i] = testDeterministicVector(text)
	}
	return result, nil
}

func (e *testEmbedder) Dimensions() int { return 8 }

func testDeterministicVector(text string) []float32 {
	const dims = 8
	vec := make([]float32, dims)
	for i, b := range []byte(text) {
		vec[i%dims] += float32(b)
	}
	// Normalize to unit length.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec
}

func sdEmbed(t *testing.T, c *Cortex, id, typ, text string) {
	t.Helper()
	vecs, _ := c.h.Cfg.Embedder.Embed(context.Background(), []string{text})
	if err := c.putEmbedding(context.Background(), id, typ, vecs[0]); err != nil {
		t.Fatal(err)
	}
}

func schemaVersion(t *testing.T, c *Cortex) int {
	t.Helper()
	var v int
	if err := c.h.DB.QueryRowContext(context.Background(), `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	return v
}
