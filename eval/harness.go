package eval

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
)

// Config names a single retrieval configuration to score. The benchmark runs
// the same eval set under each config and compares the metrics.
type Config struct {
	Name        string
	Decompose   []cortex.StructuredQuery // forced sub-queries (nil = engine default mix)
	Rerank      bool                     // MMR diversity rerank on/off
	Limit       int                      // top-k retrieved per query
}

// fixedLLM forces a deterministic decomposition so a config can isolate a
// single retrieval path (e.g. vector-only) instead of the engine's default
// multi-path mix. It implements only what Recall needs: Decompose. The other
// LLM methods are unused at recall time and return zero values.
//
// When tmpl is nil, Decompose returns nil so the engine falls back to its
// own default decomposition — this is the "hybrid (default)" config.
type fixedLLM struct {
	tmpl []cortex.StructuredQuery
}

func (l *fixedLLM) Decompose(_ context.Context, query string) ([]cortex.StructuredQuery, error) {
	if l.tmpl == nil {
		return nil, nil // engine falls back to its default mix
	}
	out := make([]cortex.StructuredQuery, len(l.tmpl))
	for i, sq := range l.tmpl {
		out[i] = cortex.StructuredQuery{
			Type:   sq.Type,
			Params: map[string]any{"query": query},
		}
	}
	return out, nil
}

func (l *fixedLLM) Extract(context.Context, string, string) (cortex.ExtractionResult, error) {
	return cortex.ExtractionResult{}, nil
}
func (l *fixedLLM) Summarize(context.Context, []string) (string, error) { return "", nil }

// Result holds the scored outcome for one config over one eval set.
type Result struct {
	Config           string  `json:"config"`
	N                int     `json:"n"`         // positive (answerable) items scored
	NAbstain         int     `json:"n_abstain"` // negative (abstain) items scored
	RecallAt1        float64 `json:"recall_at_1"`
	RecallAt3        float64 `json:"recall_at_3"`
	RecallAt5        float64 `json:"recall_at_5"`
	RecallAt10       float64 `json:"recall_at_10"`
	MRR              float64 `json:"mrr"`
	AbstentionAcc    float64 `json:"abstention_accuracy"`
	FalseAbstention  float64 `json:"false_abstention_rate"`
}

// Run scores one config over the eval set against the given Cortex instance.
// cx must be opened with the SAME embedder that produced the stored
// embeddings (otherwise vector similarity is meaningless). Run swaps in a
// fixedLLM to control decomposition, runs RecallWithStrength per item, and
// computes ranks + abstention. The caller's cx LLM is left replaced on
// return (callers should treat cx as benchmark-dedicated).
func Run(ctx context.Context, cx *cortex.Cortex, set []QA, cfg Config) (Result, error) {
	cx.SetLLM(&fixedLLM{tmpl: cfg.Decompose})

	limit := cfg.Limit
	if limit <= 0 {
		limit = 10
	}

	ranks := make([]int, len(set))
	abstained := make([]bool, len(set))

	for i, qa := range set {
		opts := []cortex.RecallOption{
			cortex.WithLimit(limit),
			cortex.WithRerank(cfg.Rerank),
		}
		rr, err := cx.RecallWithStrength(ctx, qa.Question, opts...)
		if err != nil {
			return Result{}, fmt.Errorf("eval: recall %q: %w", qa.Question, err)
		}
		abstained[i] = rr.Abstain
		if !qa.Abstain {
			contents := make([]string, len(rr.Results))
			for j, r := range rr.Results {
				contents[j] = r.Content
			}
			ranks[i] = hitRank(qa.TargetContent, contents)
		}
	}

	var nPos, nAbs int
	for _, qa := range set {
		if qa.Abstain {
			nAbs++
		} else {
			nPos++
		}
	}

	return Result{
		Config:          cfg.Name,
		N:               nPos,
		NAbstain:        nAbs,
		RecallAt1:       RecallAtK(set, ranks, 1),
		RecallAt3:       RecallAtK(set, ranks, 3),
		RecallAt5:       RecallAtK(set, ranks, 5),
		RecallAt10:      RecallAtK(set, ranks, 10),
		MRR:             MRR(set, ranks),
		AbstentionAcc:   AbstentionAccuracy(set, abstained),
		FalseAbstention: FalseAbstentionRate(set, abstained),
	}, nil
}

// DefaultConfigs returns the ablation configs the live-DB benchmark compares.
// Each isolates one lever of the recall-quality work over the existing stored
// embeddings:
//   - fts-only: keyword/FTS path only (what replaced the old LIKE match)
//   - vector-only: memory-vector path only (the embeddings that were written
//     at ingest but, before the fix, never searched)
//   - hybrid: the engine's default multi-path RRF mix, rerank off
//   - hybrid+rerank: the default mix with MMR diversity rerank on (shipped default)
//
// Confidence weighting is always on (the engine has no off switch), so it is
// not isolated here — noted as a benchmark limitation.
func DefaultConfigs(limit int) []Config {
	q := func(types ...string) []cortex.StructuredQuery {
		out := make([]cortex.StructuredQuery, len(types))
		for i, t := range types {
			out[i] = cortex.StructuredQuery{Type: t}
		}
		return out
	}
	return []Config{
		{Name: "fts-only", Decompose: q("memory_lookup"), Rerank: false, Limit: limit},
		{Name: "vector-only", Decompose: q("memory_vector"), Rerank: false, Limit: limit},
		{Name: "hybrid", Decompose: q("memory_lookup", "memory_vector"), Rerank: false, Limit: limit},
		{Name: "hybrid+rerank", Decompose: q("memory_lookup", "memory_vector"), Rerank: true, Limit: limit},
	}
}
