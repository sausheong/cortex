package tenancy

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/workset"
)

// In-memory versions of the memory listings for encrypted (and migrating)
// users: content is ciphertext, so filtering, sorting and paging run over the
// working set. A keyless handle gets model.ErrLocked.

func (c Ops) newMemoriesSinceEnc(ctx context.Context, since time.Time, perGroup int) ([]model.ConnectionNew, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	groups := map[string][]model.Memory{}
	for _, m := range ws.Memories() {
		if m.CreatedAt.After(since) {
			groups[m.WrittenBy] = append(groups[m.WrittenBy], m)
		}
	}
	var out []model.ConnectionNew
	for conn, ms := range groups {
		sort.Slice(ms, func(i, j int) bool {
			if !ms[i].CreatedAt.Equal(ms[j].CreatedAt) {
				return ms[i].CreatedAt.After(ms[j].CreatedAt)
			}
			return ms[i].ID < ms[j].ID
		})
		g := model.ConnectionNew{ConnectionID: conn, Count: len(ms)}
		for i := 0; i < len(ms) && i < perGroup; i++ {
			m := ms[i]
			g.Latest = append(g.Latest, model.Memory{ID: m.ID, Content: m.Content, CreatedAt: m.CreatedAt, WrittenBy: conn})
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Latest[0].CreatedAt, out[j].Latest[0].CreatedAt
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].ConnectionID < out[j].ConnectionID
	})
	return out, nil
}

func (c Ops) listMemoriesEnc(ctx context.Context, f model.MemoryListFilter, limit, offset int) ([]model.Memory, int, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, 0, err
	}
	lq := strings.ToLower(f.Query)
	var matched []model.Memory
	for _, m := range ws.Memories() {
		if f.Query != "" && !strings.Contains(strings.ToLower(m.Content), lq) {
			continue
		}
		if f.WrittenBy == model.NoConnection {
			if m.WrittenBy != "" {
				continue
			}
		} else if f.WrittenBy != "" && m.WrittenBy != f.WrittenBy {
			continue
		}
		if f.EntityID != "" {
			linked := false
			for _, id := range m.EntityIDs {
				if id == f.EntityID {
					linked = true
					break
				}
			}
			if !linked {
				continue
			}
		}
		if f.From != nil && m.CreatedAt.Before(*f.From) {
			continue
		}
		if f.To != nil && !m.CreatedAt.Before(*f.To) {
			continue
		}
		if f.Since != nil && !m.CreatedAt.After(*f.Since) {
			continue
		}
		matched = append(matched, m)
	}
	total := len(matched)
	if limit <= 0 {
		limit = 50
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			if f.Oldest {
				return matched[i].CreatedAt.Before(matched[j].CreatedAt)
			}
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	if offset > len(matched) {
		offset = len(matched)
	}
	matched = matched[offset:]
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]model.Memory, len(matched))
	for i, m := range matched {
		out[i] = model.Memory{ID: m.ID, Content: m.Content, Source: m.Source, Speaker: m.Speaker, WrittenBy: m.WrittenBy,
			Confidence: m.Confidence, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
	}
	return out, total, nil
}
