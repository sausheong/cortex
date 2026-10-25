package vaultart

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/workset"
)

const (
	// MaxCorrectionChars is the longest correction accepted.
	MaxCorrectionChars = 2000
	// correctionDedupe is how long an identical correction counts as a
	// double submit of the first.
	correctionDedupe = 30 * time.Second
	// correctionBy is recorded as the deleter of an undone correction.
	correctionBy = "vault"
)

// Correction errors. Both are safe to show.
var (
	ErrBadCorrection = errors.New("vaultart: correction is empty or too long")
	ErrNotCorrection = errors.New("vaultart: not a correction of this article")
)

// Correction is one live correction memory of a subject.
type Correction struct {
	ID, Text string
	At       time.Time
}

// strongestMember is the member with the most live memories, ties broken by
// name then id (the rule topics use for their title).
func strongestMember(set *workset.Set, sub Subject) string {
	ix := buildIndex(set)
	best, bestN, bestName := "", -1, ""
	for _, id := range sub.Members {
		n := len(ix.mems[id])
		name := ""
		if e, ok := set.EntityByID(id); ok {
			name = e.Name
		}
		if best == "" || n > bestN || (n == bestN && (name < bestName || (name == bestName && id < best))) {
			best, bestN, bestName = id, n, name
		}
	}
	return best
}

// recentCorrection finds a live correction of the subject with the same text
// created inside the dedupe window. It reads the database, not the working
// set, so a double submit is caught across restarts and processes.
func recentCorrection(ctx context.Context, cx *cortex.Cortex, sub Subject, text string) (string, error) {
	since := time.Now().UTC().Add(-correctionDedupe)
	for _, eid := range sub.entityIDs() {
		ms, _, err := cx.ListMemories(ctx, model.MemoryListFilter{EntityID: eid, Since: &since}, 50, 0)
		if err != nil {
			return "", err
		}
		for _, m := range ms {
			if m.Source == model.SourceVaultCorrection && strings.TrimSpace(m.Content) == text &&
				m.ExpiredAt == nil && m.InvalidAt == nil {
				return m.ID, nil
			}
		}
	}
	return "", nil
}

// AddCorrection stores text as a memory in the owner's voice linked to the
// subject (for a topic, to its strongest member), embeds it, and marks the
// article stale. No extractor is called. An identical correction made in the
// last 30 seconds is returned instead of saved again.
func AddCorrection(ctx context.Context, cx *cortex.Cortex, sub Subject, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > MaxCorrectionChars {
		return "", ErrBadCorrection
	}
	h := cx.StoreHandle()
	set, err := workset.For(ctx, h)
	if err != nil {
		return "", err
	}
	if id, err := recentCorrection(ctx, cx, sub, text); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}
	link := sub.ID
	if sub.Kind == "topic" {
		if link = strongestMember(set, sub); link == "" {
			return "", ErrNotCorrection
		}
	}
	m := &model.Memory{
		Content:    text,
		Speaker:    "user",
		Source:     model.SourceVaultCorrection,
		EntityIDs:  []string{link},
		Confidence: 1,
	}
	if err := cx.PutMemory(ctx, m); err != nil {
		return "", err
	}
	if h.Cfg.Embedder != nil {
		var names []string
		if e, ok := set.EntityByID(link); ok {
			names = append(names, e.Name)
		}
		// A failed embed is backfilled by nightly maintenance; the
		// correction itself is saved either way.
		_ = memory.New(h).EmbedAndStoreBatch(ctx, []memory.EmbedTarget{
			{RefID: m.ID, RefType: "memory", Text: memory.AugmentForEmbedding(text, names)},
		})
	}
	if err := h.MarkArticleStale(ctx, sub.ID); err != nil {
		return m.ID, err
	}
	return m.ID, nil
}

// ListCorrections returns the live corrections of the subject, newest first.
func ListCorrections(set *workset.Set, sub Subject) []Correction {
	var out []Correction
	for _, m := range buildIndex(set).memories(sub) {
		if m.Source == model.SourceVaultCorrection {
			out = append(out, Correction{ID: m.ID, Text: m.Content, At: m.CreatedAt})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// UndoCorrection soft-deletes a correction (it can be restored from Deleted)
// and marks the article stale. It refuses a memory that is not a vault
// correction linked to the subject, or to any member of a topic.
func UndoCorrection(ctx context.Context, cx *cortex.Cortex, sub Subject, memoryID string) error {
	ms, _, err := cx.ListMemories(ctx, model.MemoryListFilter{ID: memoryID}, 1, 0)
	if err != nil {
		return err
	}
	if len(ms) != 1 {
		return ErrNotCorrection
	}
	m := ms[0]
	if m.Source != model.SourceVaultCorrection || m.ExpiredAt != nil || m.InvalidAt != nil {
		return ErrNotCorrection
	}
	linked := false
	for _, id := range sub.entityIDs() {
		if slices.Contains(m.EntityIDs, id) {
			linked = true
			break
		}
	}
	if !linked {
		return ErrNotCorrection
	}
	if _, n, err := cx.SoftDeleteMemories(ctx, model.DeleteFilter{IDs: []string{memoryID}}, correctionBy); err != nil {
		return err
	} else if n == 0 {
		return ErrNotCorrection
	}
	return cx.StoreHandle().MarkArticleStale(ctx, sub.ID)
}
