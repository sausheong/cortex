//go:build bench

package cortex

// Multi-user vector benchmark: 2 users x 100K random 512-dim halfvec memory
// embeddings (generation 1), partial HNSW index via EnsureVectorIndex.
//
//	CORTEX_BENCH_DATABASE_URL=postgres://postgres:bench@localhost:55432/postgres?sslmode=disable \
//	CORTEX_BENCH_CONTAINER=cortex-bench \
//	go test -tags bench -run TestBenchTwoUsers -v -timeout 3h .
//
// CORTEX_BENCH_N overrides the per-user row count. CORTEX_BENCH_CONTAINER, if
// set, is a docker container whose cgroup memory is sampled.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const benchDims = 512

type benchEmbedder struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func (e *benchEmbedder) Dimensions() int { return benchDims }
func (e *benchEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = randVec(e.rng)
	}
	return out, nil
}

// benchLLM is a stub: Decompose returns nothing so Recall uses its default
// four-way fallback (keyword, memory lookup, memory vector, chunk vector).
type benchLLM struct{}

func (benchLLM) Extract(context.Context, string, string) (ExtractionResult, error) {
	return ExtractionResult{}, nil
}
func (benchLLM) Decompose(context.Context, string) ([]StructuredQuery, error) { return nil, nil }
func (benchLLM) Summarize(context.Context, []string) (string, error)          { return "", nil }

func randVec(r *rand.Rand) []float32 {
	v := make([]float32, benchDims)
	var n float64
	for i := range v {
		x := float32(r.NormFloat64())
		v[i] = x
		n += float64(x) * float64(x)
	}
	inv := float32(1 / sqrt(n))
	for i := range v {
		v[i] *= inv
	}
	return v
}

func sqrt(x float64) float64 {
	z := x / 2
	for i := 0; i < 30; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func pct(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s)-1) * p)
	return s[idx]
}

func benchMem(t *testing.T, label string) {
	name := os.Getenv("CORTEX_BENCH_CONTAINER")
	if name == "" {
		return
	}
	sh := `cur=$(cat /sys/fs/cgroup/memory.current); pk=$(cat /sys/fs/cgroup/memory.peak 2>/dev/null); sw=$(cat /sys/fs/cgroup/memory.swap.current 2>/dev/null); ` +
		`rss=$(awk '/^VmRSS/ {s+=$2} END {print s}' /proc/[0-9]*/status); ` +
		`anon=$(awk '/^anon /{print $2}' /sys/fs/cgroup/memory.stat); file=$(awk '/^file /{print $2}' /sys/fs/cgroup/memory.stat); ` +
		`echo "cgroup_current=$((cur/1048576))MB cgroup_peak=$((pk/1048576))MB swap=$((sw/1048576))MB anon=$((anon/1048576))MB file_cache=$((file/1048576))MB sum_proc_rss_kb=$rss"`
	out, err := exec.Command("docker", "exec", name, "sh", "-c", sh).CombinedOutput()
	t.Logf("MEM[%s] %s (err=%v)", label, strings.TrimSpace(string(out)), err)
}

var vocab = func() []string {
	w := make([]string, 4000)
	for i := range w {
		w[i] = "w" + strconv.FormatInt(int64(i)*7919, 36)
	}
	return w
}()

// loadUser inserts n memories and n generation-1 embeddings for the user.
func loadUser(t *testing.T, ctx context.Context, c *Cortex, n int, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	const batch = 500
	for off := 0; off < n; off += batch {
		m := batch
		if off+m > n {
			m = n - off
		}
		ids := make([]string, m)
		contents := make([]string, m)
		vecs := make([]string, m)
		for i := 0; i < m; i++ {
			ids[i] = fmt.Sprintf("m_%s_%d", c.h.UserID, off+i)
			var sb strings.Builder
			for w := 0; w < 14; w++ {
				if w > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(vocab[rng.Intn(len(vocab))])
			}
			contents[i] = sb.String()
			vecs[i] = vectorLiteral(randVec(rng))
		}
		tx, err := c.h.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memories (id, user_id, content, source) SELECT id, ?, content, 'bench' FROM unnest(?::text[], ?::text[]) AS u(id, content)`,
			c.h.UserID, ids, contents); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO embeddings (id, user_id, ref_id, ref_type, embedding, dims, generation)
			 SELECT 'e_' || id, ?, id, 'memory', v::halfvec, ?, 1 FROM unnest(?::text[], ?::text[]) AS u(id, v)`,
			c.h.UserID, benchDims, ids, vecs); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func measure(t *testing.T, label string, q int, fn func(i int) error) {
	d := make([]time.Duration, 0, q)
	for i := 0; i < q; i++ {
		s := time.Now()
		if err := fn(i); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		d = append(d, time.Since(s))
	}
	t.Logf("RESULT %-44s n=%d p50=%v p95=%v max=%v", label, q, pct(d, .5).Round(10*time.Microsecond), pct(d, .95).Round(10*time.Microsecond), pct(d, 1).Round(10*time.Microsecond))
}

func TestBenchTwoUsers(t *testing.T) {
	dsn := os.Getenv("CORTEX_BENCH_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CORTEX_BENCH_DATABASE_URL")
	}
	n := 100000
	if s := os.Getenv("CORTEX_BENCH_N"); s != "" {
		n, _ = strconv.Atoi(s)
	}
	const queries = 200
	ctx := context.Background()
	emb := &benchEmbedder{rng: rand.New(rand.NewSource(99))}
	root, err := Open(dsn, WithEmbedder(emb), WithLLM(benchLLM{}), WithMigrationTimeout(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	ua, err := root.CreateUser(ctx, "", "a@bench.test", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	ub, err := root.CreateUser(ctx, "", "b@bench.test", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	a := root.ForUser(ua.ID, WithEmbedder(emb), WithLLM(benchLLM{})).WithGeneration(1)
	b := root.ForUser(ub.ID, WithEmbedder(emb), WithLLM(benchLLM{})).WithGeneration(1)

	benchMem(t, "start")
	s := time.Now()
	loadUser(t, ctx, a, n, 1)
	t.Logf("TIMING load user A (%d rows, no index yet): %v", n, time.Since(s).Round(time.Second))
	if _, err := root.h.DB.ExecContext(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}

	// Index build for A's 100K (per-dims partial HNSW, as the jobs package does).
	s = time.Now()
	for _, rt := range []string{"entity", "memory", "chunk"} {
		if err := a.EnsureVectorIndex(ctx, benchDims, rt); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("TIMING EnsureVectorIndex (%d rows, 3 ref types): %v", n, time.Since(s).Round(time.Millisecond))
	var isz string
	_ = root.h.DB.QueryRowContext(ctx, `SELECT pg_size_pretty(pg_relation_size('emb_hnsw_memory_512'))`).Scan(&isz)
	t.Logf("SIZE emb_hnsw_memory_512 after A: %s", isz)
	benchMem(t, "after A index build")

	run := func(tag string, u *Cortex) {
		// warm-up
		for i := 0; i < 10; i++ {
			v := randVec(emb.rng)
			if _, err := u.searchVectorRaw(ctx, v, "memory", 10); err != nil {
				t.Fatal(err)
			}
		}
		qs := make([][]float32, queries)
		for i := range qs {
			qs[i] = randVec(emb.rng)
		}
		measure(t, tag+" searchVectorRaw(memory) top10", queries, func(i int) error {
			r, err := u.searchVectorRaw(ctx, qs[i], "memory", 10)
			if err == nil && len(r) < 10 {
				err = fmt.Errorf("only %d results", len(r))
			}
			return err
		})
		measure(t, tag+" SearchMemoryVector top20 (hydrated)", queries, func(i int) error {
			r, err := u.SearchMemoryVector(ctx, "bench query", 20)
			if err == nil && len(r) == 0 {
				err = fmt.Errorf("no results")
			}
			return err
		})
		measure(t, tag+" SearchVector (chunk path, no chunks)", queries, func(i int) error {
			_, err := u.SearchVector(ctx, "bench query", 20)
			return err
		})
		measure(t, tag+" Recall (stub llm+embedder)", queries, func(i int) error {
			q := vocab[(i*13)%len(vocab)] + " " + vocab[(i*29+7)%len(vocab)] + " " + vocab[(i*31+3)%len(vocab)]
			r, err := u.Recall(ctx, q)
			if err == nil && len(r) == 0 {
				err = fmt.Errorf("no results")
			}
			return err
		})
	}

	run("A alone     ", a)
	benchMem(t, "after A queries")

	s = time.Now()
	loadUser(t, ctx, b, n, 2)
	t.Logf("TIMING load user B (%d rows, incremental into existing index): %v", n, time.Since(s).Round(time.Second))
	if _, err := root.h.DB.ExecContext(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	_ = root.h.DB.QueryRowContext(ctx, `SELECT pg_size_pretty(pg_relation_size('emb_hnsw_memory_512'))`).Scan(&isz)
	t.Logf("SIZE emb_hnsw_memory_512 after A+B: %s", isz)
	benchMem(t, "after B load")

	run("A with B   ", a)
	run("B with A   ", b)
	benchMem(t, "after all queries")

	// Fresh build over both users (cold-build cost for 200K rows).
	if _, err := root.h.DB.ExecContext(ctx, `DROP INDEX emb_hnsw_memory_512`); err != nil {
		t.Fatal(err)
	}
	s = time.Now()
	if err := a.EnsureVectorIndex(ctx, benchDims, "memory"); err != nil {
		t.Fatal(err)
	}
	t.Logf("TIMING EnsureVectorIndex rebuild (%d rows, memory): %v", 2*n, time.Since(s).Round(time.Millisecond))
	benchMem(t, "after 200K rebuild")
	run("A with B (rebuilt idx)", a)
}
