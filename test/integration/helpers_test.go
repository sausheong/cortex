package integration_test

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/maintain"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/tenancy"
	"github.com/sausheong/cortex/internal/testutil"
)

// Helpers shared by the black-box integration tests. Raw access to the store
// handle goes through testutil.H; the wrappers below stand in for methods that
// moved to internal/store, internal/memory and internal/maintain.

// Re-exports used by moved tests.
const (
	reembedLockSQL      = memory.ReembedLockSQL
	attrProfile         = maintain.AttrProfile
	attrProfiled        = maintain.AttrProfiled
	profileStaticPrompt = maintain.ProfileStaticPrompt
)

func putEmbedding(c *Cortex, ctx context.Context, refID, refType string, v []float32) error {
	return memory.New(testutil.H(c)).PutEmbedding(ctx, refID, refType, v)
}

func getMemoryByID(c *Cortex, ctx context.Context, id string) (*Memory, error) {
	return memory.New(testutil.H(c)).GetMemoryByID(ctx, id)
}

func embeddingByRef(c *Cortex, ctx context.Context, refID, refType string) []float32 {
	return memory.New(testutil.H(c)).EmbeddingByRef(ctx, refID, refType)
}

func searchVectorRaw(c *Cortex, ctx context.Context, queryVec []float32, refType string, limit int) ([]memory.VectorResult, error) {
	return memory.New(testutil.H(c)).SearchVectorRaw(ctx, queryVec, refType, limit)
}

func searchMemoriesMode(c *Cortex, ctx context.Context, query string, limit int, mode model.TemporalMode) ([]Memory, error) {
	return memory.New(testutil.H(c)).SearchMemoriesMode(ctx, query, limit, mode)
}

func neighbourhood(c *Cortex, ctx context.Context, seeds []string, hops int, mode model.TemporalMode) (map[string]int, error) {
	return memory.New(testutil.H(c)).Neighbourhood(ctx, seeds, hops, mode)
}

func profileEligibleIDs(c *Cortex, ctx context.Context) ([]string, error) {
	return maintain.New(testutil.H(c)).ProfileEligibleIDs(ctx)
}

func distillBucket(c *Cortex, ctx context.Context, name, instruction string, mems []Memory) ([]string, bool) {
	return maintain.New(testutil.H(c)).DistillBucket(ctx, name, instruction, mems)
}

func renderLintMarkdown(r LintReport) string { return RenderLintMarkdown(r) }

func vectorLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// testMode is "encrypted" while forEachMode runs. The package-internal tests
// cannot use internal/testutil.ForEachMode (testutil imports this package), so
// they run through forEachMode and the openers below, which wrap the freshly
// opened handle with modeHandle. Plaintext accounts no longer exist (migration
// 012): every account is encrypted.
var testMode string

// forEachMode runs fn as the subtest "encrypted".
func forEachMode(t *testing.T, fn func(*testing.T)) {
	t.Helper()
	t.Run("encrypted", func(t *testing.T) {
		testMode = "encrypted"
		t.Cleanup(func() { testMode = "" })
		fn(t)
	})
}

// modeHandle creates a fresh user in state 'encrypted' and returns a handle
// for it holding a user key.
func modeHandle(t *testing.T, c *Cortex) *Cortex { return modeHandleTB(t, c) }

func modeHandleTB(t testing.TB, c *Cortex) *Cortex {
	t.Helper()
	u, err := c.CreateUser(context.Background(), "enc-sub", "enc@x.test", "enc", true)
	if err != nil {
		t.Fatal(err)
	}
	return keyedHandle(t, c, u.ID)
}

// keyedHandle makes user id an encrypted account and returns a handle for it
// holding a fresh user key. opts apply to the returned handle.
func keyedHandle(t testing.TB, root *Cortex, id string, opts ...Option) *Cortex {
	t.Helper()
	cx, _ := keyedHandleUK(t, root, id, opts...)
	return cx
}

func keyedHandleUK(t testing.TB, root *Cortex, id string, opts ...Option) (*Cortex, *keys.UK) {
	t.Helper()
	if _, err := testutil.H(root).DB.ExecContext(context.Background(), `UPDATE users SET encryption_state = 'encrypted' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	cx := root.ForUser(id, opts...).WithKey(uk, "encrypted")
	t.Cleanup(cx.Release)
	return cx, uk
}

// sealOwner creates the user's sealed owner entity the way enrollment does and
// records it in users.owner_entity_id.
func sealOwner(t testing.TB, root *Cortex, uk *keys.UK, userID, display string) {
	t.Helper()
	ctx := context.Background()
	id := store.NewID()
	seal := func(col, v string) []byte {
		ct, err := uk.Seal(userID, store.TblEntities, id, col, []byte(v))
		if err != nil {
			t.Fatal(err)
		}
		return ct
	}
	if _, err := testutil.H(root).DB.ExecContext(ctx,
		`INSERT INTO entities (id, user_id, is_owner, name_ct, type_ct, source_ct, attributes_ct, name_blind, type_blind)
		 VALUES (?, ?, TRUE, ?, ?, ?, ?, ?, ?)`,
		id, userID, seal("name", display), seal("type", "person"), seal("source", "owner"), seal("attributes", "{}"),
		uk.Blind(store.NormName(display)), uk.Blind(store.NormName("person"))); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.H(root).DB.ExecContext(ctx, `UPDATE users SET owner_entity_id = ? WHERE id = ?`, id, userID); err != nil {
		t.Fatal(err)
	}
}

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
	ownerID, err := tenancy.New(testutil.H(c)).OwnerEntityID(context.Background())
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
	if _, err := testutil.H(c).DB.ExecContext(context.Background(), `UPDATE entities SET is_owner = TRUE WHERE id = ?`, id); err != nil {
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
	vecs, _ := testutil.H(c).Cfg.Embedder.Embed(context.Background(), []string{text})
	if err := putEmbedding(c, context.Background(), id, typ, vecs[0]); err != nil {
		t.Fatal(err)
	}
}

func schemaVersion(t *testing.T, c *Cortex) int {
	t.Helper()
	var v int
	if err := testutil.H(c).DB.QueryRowContext(context.Background(), `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	return v
}
