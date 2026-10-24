package maintain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vecmath"
)

const (
	compactMergeSim    = 0.9
	compactCombineSim  = 0.8
	compactRollupAge   = 90 * 24 * time.Hour
	compactMaxCands    = 2000
	compactMaxGroup    = 20
	compactMinRollup   = 3
	compactSourcePrefx = "compaction:"
)

type compactCand struct {
	id, content string
	static      bool
	created     time.Time
	vec         []float32
}

var compactPrompts = map[string]string{
	model.CompactMerge:   "These memories are near-duplicates. Merge them into one concise memory that keeps every distinct fact. Reply with the merged memory only.",
	model.CompactCombine: "These memories are closely related. Combine them into one concise memory that keeps every distinct fact. Reply with the combined memory only.",
	model.CompactRollup:  "These are older memories about one subject. Summarize them into one concise memory that keeps the lasting facts. Reply with the summary only.",
}

// ProposeCompaction proposes merges, combinations and rollups of this user's
// live memories using the user's LLM. Only the active embedding generation is
// read. Nothing is written.
func (c Ops) ProposeCompaction(ctx context.Context) ([]model.Change, error) {
	if c.Cfg.LLM == nil {
		return nil, fmt.Errorf("cortex: no LLM configured")
	}
	cands, err := c.compactCandidates(ctx)
	if err != nil {
		return nil, err
	}

	// Union-find clusters by embedding similarity.
	n := len(cands)
	parentMerge := make([]int, n)
	parentComb := make([]int, n)
	for i := range parentMerge {
		parentMerge[i], parentComb[i] = i, i
	}
	var find func(p []int, x int) int
	find = func(p []int, x int) int {
		for p[x] != x {
			p[x] = p[p[x]]
			x = p[x]
		}
		return x
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			s := vecmath.Cosine(cands[i].vec, cands[j].vec)
			if s >= compactMergeSim {
				parentMerge[find(parentMerge, i)] = find(parentMerge, j)
			}
			if s >= compactCombineSim {
				parentComb[find(parentComb, i)] = find(parentComb, j)
			}
		}
	}
	group := func(p []int) [][]int {
		m := map[int][]int{}
		var order []int
		for i := 0; i < n; i++ {
			r := find(p, i)
			if _, ok := m[r]; !ok {
				order = append(order, r)
			}
			m[r] = append(m[r], i)
		}
		var out [][]int
		for _, r := range order {
			if len(m[r]) > 1 {
				out = append(out, m[r])
			}
		}
		return out
	}

	used := map[string]bool{}
	var changes []model.Change
	add := func(kind string, idx []int) error {
		if len(idx) > compactMaxGroup {
			idx = idx[:compactMaxGroup]
		}
		ch, err := c.compactChange(ctx, kind, cands, idx)
		if err != nil {
			return err
		}
		if ch == nil {
			return nil
		}
		for _, id := range ch.SourceIDs {
			used[id] = true
		}
		changes = append(changes, *ch)
		return nil
	}
	for _, g := range group(parentMerge) {
		if err := add(model.CompactMerge, g); err != nil {
			return nil, err
		}
	}
	for _, g := range group(parentComb) {
		var free []int
		for _, i := range g {
			if !used[cands[i].id] {
				free = append(free, i)
			}
		}
		if len(free) > 1 {
			if err := add(model.CompactCombine, free); err != nil {
				return nil, err
			}
		}
	}

	// Rollups: old non-static memories grouped by entity.
	cutoff := time.Now().Add(-compactRollupAge)
	rows, err := c.DB.QueryContext(ctx,
		`SELECT me.entity_id, m.id
		 FROM memories m JOIN memory_entities me ON me.memory_id = m.id AND me.user_id = m.user_id
		 WHERE m.user_id = ? AND m.deleted_at IS NULL AND m.static = false AND m.created_at < ?
		 ORDER BY me.entity_id, m.created_at, m.id`, c.UserID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("cortex: rollup candidates: %w", err)
	}
	type rm struct{ id, content string }
	byEntity := map[string][]rm{}
	var entities []string
	for rows.Next() {
		var e string
		var m rm
		if err := rows.Scan(&e, &m.id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan rollup candidate: %w", err)
		}
		// Content is ciphertext: filled from the working set.
		if m.content, err = c.memoryContent(ctx, m.id); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := byEntity[e]; !ok {
			entities = append(entities, e)
		}
		byEntity[e] = append(byEntity[e], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate rollup candidates: %w", err)
	}
	sort.Strings(entities)
	for _, e := range entities {
		var free []rm
		for _, m := range byEntity[e] {
			if !used[m.id] {
				free = append(free, m)
			}
		}
		if len(free) < compactMinRollup {
			continue
		}
		if len(free) > compactMaxGroup {
			free = free[:compactMaxGroup]
		}
		ch := model.Change{Kind: model.CompactRollup}
		var texts []string
		for _, m := range free {
			ch.SourceIDs = append(ch.SourceIDs, m.id)
			ch.Sources = append(ch.Sources, m.content)
			texts = append(texts, m.content)
		}
		res, err := c.compactSummarize(ctx, model.CompactRollup, texts)
		if err != nil {
			return nil, err
		}
		if res == "" {
			continue
		}
		ch.Result = res
		for _, id := range ch.SourceIDs {
			used[id] = true
		}
		changes = append(changes, ch)
	}
	return changes, nil
}

func (c Ops) compactSummarize(ctx context.Context, kind string, texts []string) (string, error) {
	in := append([]string{compactPrompts[kind]}, texts...)
	out, err := c.Cfg.LLM.Summarize(ctx, in)
	if err != nil {
		return "", fmt.Errorf("cortex: compaction summarize: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (c Ops) compactChange(ctx context.Context, kind string, cands []compactCand, idx []int) (*model.Change, error) {
	ch := &model.Change{Kind: kind}
	var texts []string
	for _, i := range idx {
		ch.SourceIDs = append(ch.SourceIDs, cands[i].id)
		ch.Sources = append(ch.Sources, cands[i].content)
		texts = append(texts, cands[i].content)
		ch.Static = ch.Static || cands[i].static
	}
	res, err := c.compactSummarize(ctx, kind, texts)
	if err != nil {
		return nil, err
	}
	if res == "" {
		return nil, nil
	}
	ch.Result = res
	return ch, nil
}

// compactCandidates loads live memories with an active-generation embedding,
// newest first, capped.
func (c Ops) compactCandidates(ctx context.Context) ([]compactCand, error) {
	return c.compactCandidatesEnc(ctx)
}

// ApplyCompaction writes each change's result as a new memory with a derives
// edge to every source, and soft-deletes the sources (by "compaction"), all
// in one batch. Every source must be one of this user's live memories, else
// ErrNotFound is returned and nothing is written.
func (c Ops) ApplyCompaction(ctx context.Context, changes []model.Change) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	seen := map[string]bool{}
	var all []string
	for _, ch := range changes {
		if len(ch.SourceIDs) == 0 || strings.TrimSpace(ch.Result) == "" {
			return "", fmt.Errorf("cortex: compaction change needs sources and a result")
		}
		for _, id := range ch.SourceIDs {
			if seen[id] {
				return "", fmt.Errorf("cortex: memory %s appears in more than one change", id)
			}
			seen[id] = true
			all = append(all, id)
		}
	}
	// Embed results first so a failure writes nothing.
	var vecs [][]float32
	if c.Cfg.Embedder != nil {
		texts := make([]string, len(changes))
		for i, ch := range changes {
			texts[i] = ch.Result
		}
		v, err := c.Cfg.Embedder.Embed(ctx, texts)
		if err != nil {
			return "", fmt.Errorf("cortex: embed compaction results: %w", err)
		}
		if len(v) != len(texts) {
			return "", fmt.Errorf("cortex: embedder returned %d vectors for %d texts", len(v), len(texts))
		}
		vecs = v
	}

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	ph := make([]string, len(all))
	args := []any{c.UserID}
	for i, id := range all {
		ph[i] = "?"
		args = append(args, id)
	}
	var live int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM memories WHERE user_id = ? AND deleted_at IS NULL AND id IN (`+strings.Join(ph, ",")+`)`,
		args...).Scan(&live); err != nil {
		return "", fmt.Errorf("cortex: check compaction sources: %w", err)
	}
	if live != len(all) {
		return "", model.NotFound("cortex: compaction source not found")
	}

	batchID, err := graph.NewBatch(ctx, tx, c.UserID, "compaction")
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	resultIDs := make([]string, len(changes))
	for i, ch := range changes {
		rid := store.NewID()
		resultIDs[i] = rid
		if err := memory.New(c.Handle).InsertMemoryTx(ctx, tx, &model.Memory{
			ID: rid, Content: strings.TrimSpace(ch.Result), Source: compactSourcePrefx + batchID,
			Confidence: 1.0, CreatedAt: now, UpdatedAt: now, Static: ch.Static}); err != nil {
			return "", fmt.Errorf("cortex: insert compaction result: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_entities (user_id, memory_id, entity_id)
			 SELECT DISTINCT ?::text, ?::text, entity_id FROM memory_entities
			 WHERE user_id = ? AND memory_id IN (SELECT unnest(?::text[]))`,
			c.UserID, rid, c.UserID, pgTextArray(ch.SourceIDs)); err != nil {
			return "", fmt.Errorf("cortex: link compaction entities: %w", err)
		}
		for _, sid := range ch.SourceIDs {
			if err := memory.New(c.Handle).InsertMemoryEdgeTx(ctx, tx, &model.MemoryEdge{
				ID: store.NewID(), SourceID: rid, TargetID: sid, Type: model.EdgeDerives, Source: "compaction", CreatedAt: now}); err != nil {
				return "", fmt.Errorf("cortex: insert derives edge: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE memories SET deleted_at = now(), deleted_by = 'compaction', delete_batch_id = ?
		 WHERE user_id = ? AND deleted_at IS NULL AND id IN (`+strings.Join(ph, ",")+`)`,
		append([]any{batchID, c.UserID}, args[1:]...)...); err != nil {
		return "", fmt.Errorf("cortex: soft delete compaction sources: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("cortex: commit compaction: %w", err)
	}
	for i, v := range vecs {
		if err := memory.New(c.Handle).PutEmbedding(ctx, resultIDs[i], "memory", v); err != nil {
			return batchID, fmt.Errorf("cortex: store compaction embedding: %w", err)
		}
	}
	return batchID, nil
}

// UndoCompaction restores the sources of a compaction batch and soft-deletes
// the memories the compaction wrote. A batch that is not this user's is
// ErrNotFound.
func (c Ops) UndoCompaction(ctx context.Context, batchID string) error {
	if err := graph.New(c.Handle).RestoreBatch(ctx, batchID); err != nil {
		return err
	}
	if err := c.retireCompactionResultsEnc(ctx, compactSourcePrefx+batchID); err != nil {
		return fmt.Errorf("cortex: retire compaction results: %w", err)
	}
	return nil
}

func pgTextArray(ids []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(id))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
