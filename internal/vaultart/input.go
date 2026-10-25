package vaultart

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/workset"
)

// Article input constants.
const (
	// PromptVersion is part of every fingerprint; bump it to rewrite every
	// article after a prompt change.
	PromptVersion = "1"
	// DefaultBudgetTokens is the memory budget of one article call.
	DefaultBudgetTokens = 6000
	// MaxCorrections is how many vault corrections are pinned ahead of the
	// budget.
	MaxCorrections = 25

	// CorrectionSource marks a memory the user wrote through "Correct this".
	CorrectionSource = "vault correction"

	maxMemoryChars = 2000
	maxRelations   = 60
)

// Subject is what one article is about: a single entity, or a topic made of
// member entities. Kind is "entity" or "topic". For an entity ID is the
// entity id and Members is empty; for a topic ID is the topic id and Members
// holds the member entity ids.
type Subject struct {
	ID      string
	Group   Group
	Kind    string
	Name    string
	Members []string
}

// entityIDs returns the entities whose data makes up the subject.
func (s Subject) entityIDs() []string {
	if s.Kind == "topic" {
		return s.Members
	}
	return []string{s.ID}
}

// index is a one-pass lookup of the live data per entity, so every subject
// is assembled in O(its own data) rather than a scan of the whole set.
type index struct {
	set     *workset.Set
	mems    map[string][]*model.Memory // entity id -> live memories
	rels    map[string][]*model.Relationship
	aliases map[string][]string
}

func buildIndex(set *workset.Set) *index {
	ix := &index{
		set:     set,
		mems:    map[string][]*model.Memory{},
		rels:    map[string][]*model.Relationship{},
		aliases: map[string][]string{},
	}
	live := set.MemoriesMode(model.TemporalMode{})
	for i := range live {
		m := &live[i]
		seen := map[string]bool{}
		for _, id := range m.EntityIDs {
			if !seen[id] {
				seen[id] = true
				ix.mems[id] = append(ix.mems[id], m)
			}
		}
	}
	rels := set.Relationships()
	for i := range rels {
		r := &rels[i]
		ix.rels[r.SourceID] = append(ix.rels[r.SourceID], r)
		if r.TargetID != r.SourceID {
			ix.rels[r.TargetID] = append(ix.rels[r.TargetID], r)
		}
	}
	for _, a := range set.Aliases() {
		ix.aliases[a.EntityID] = append(ix.aliases[a.EntityID], a.Value)
	}
	for k := range ix.aliases {
		sort.Strings(ix.aliases[k])
	}
	return ix
}

// memories returns the live memories of the subject, deduplicated across
// members, in a stable id order.
func (ix *index) memories(sub Subject) []*model.Memory {
	var out []*model.Memory
	seen := map[string]bool{}
	for _, id := range sub.entityIDs() {
		for _, m := range ix.mems[id] {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MemoryIDs returns the ids of every live memory of the subject: the valid
// citation targets of its article.
func MemoryIDs(set *workset.Set, sub Subject) map[string]bool {
	out := map[string]bool{}
	ix := buildIndex(set)
	for _, m := range ix.memories(sub) {
		out[m.ID] = true
	}
	return out
}

func approxTokens(s string) int { return (utf8.RuneCountInString(s) + 3) / 4 }

func truncateChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "..."
}

func whenOf(m *model.Memory) string {
	t := m.CreatedAt
	if m.ValidAt != nil {
		t = *m.ValidAt
	}
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

func toArticleMemory(m *model.Memory, correction bool) model.ArticleMemory {
	return model.ArticleMemory{
		ID:         m.ID,
		Content:    truncateChars(m.Content, maxMemoryChars),
		When:       whenOf(m),
		Speaker:    m.Speaker,
		Source:     m.Source,
		Confidence: m.Confidence,
		Correction: correction,
	}
}

// BuildInput assembles what the writer is told about the subject. Vault
// corrections (up to MaxCorrections, newest first) are pinned ahead of the
// budget so a correction can never be crowded out. The remaining live
// memories are ranked by confidence, then recency, then static first, and
// taken in that order while they fit budgetTokens (about four characters a
// token). A single memory over 2000 characters is truncated in the prompt
// only. total is the approximate token count of the memories included.
func BuildInput(set *workset.Set, sub Subject, ownerName string, linkable []Candidate, s Settings, budgetTokens int) (in model.ArticleInput, usedMemoryIDs []string, total int) {
	s = s.Normalize()
	if budgetTokens <= 0 {
		budgetTokens = DefaultBudgetTokens
	}
	ix := buildIndex(set)
	all := ix.memories(sub)

	var corr, rest []*model.Memory
	for _, m := range all {
		if m.Source == CorrectionSource {
			corr = append(corr, m)
		} else {
			rest = append(rest, m)
		}
	}
	sort.SliceStable(corr, func(i, j int) bool {
		if !corr[i].CreatedAt.Equal(corr[j].CreatedAt) {
			return corr[i].CreatedAt.After(corr[j].CreatedAt)
		}
		return corr[i].ID < corr[j].ID
	})
	if len(corr) > MaxCorrections {
		rest = append(rest, corr[MaxCorrections:]...)
		corr = corr[:MaxCorrections]
	}
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		if a.Static != b.Static {
			return a.Static
		}
		return a.ID < b.ID
	})

	in.Memories = []model.ArticleMemory{}
	for _, m := range corr {
		am := toArticleMemory(m, true)
		total += approxTokens(am.Content)
		in.Memories = append(in.Memories, am)
		usedMemoryIDs = append(usedMemoryIDs, m.ID)
	}
	for _, m := range rest {
		am := toArticleMemory(m, false)
		cost := approxTokens(am.Content)
		if total+cost > budgetTokens {
			break
		}
		total += cost
		in.Memories = append(in.Memories, am)
		usedMemoryIDs = append(usedMemoryIDs, m.ID)
	}

	in.Owner = ownerName
	in.Subject = model.ArticleSubject{Kind: sub.Kind, Name: sub.Name}
	if sub.Kind == "topic" {
		in.Subject.Type = "topic"
		for _, id := range sub.Members {
			if e, ok := set.EntityByID(id); ok {
				in.Subject.Members = append(in.Subject.Members, e.Name)
			}
		}
		sort.Strings(in.Subject.Members)
	} else if e, ok := set.EntityByID(sub.ID); ok {
		in.Subject.Name = e.Name
		in.Subject.Type = e.Type
		if len(e.Attributes) > 0 {
			in.Attributes = e.Attributes
		}
		in.Aliases = append([]string(nil), ix.aliases[sub.ID]...)
	}

	in.Relations = ix.relations(sub)
	in.Linkable = linkableNames(ix, sub, linkable)
	return in, usedMemoryIDs, total
}

// relations lists the current edges that leave or enter the subject, with
// the other end's name and descriptor. Edges inside a topic are skipped.
func (ix *index) relations(sub Subject) []model.ArticleRelation {
	now := time.Now()
	ids := sub.entityIDs()
	mine := map[string]bool{}
	for _, id := range ids {
		mine[id] = true
	}
	seen := map[string]bool{}
	out := []model.ArticleRelation{}
	for _, id := range ids {
		for _, r := range ix.rels[id] {
			if seen[r.ID] || (r.InvalidAt != nil && !r.InvalidAt.After(now)) {
				continue
			}
			seen[r.ID] = true
			dir, other := "out", r.TargetID
			if !mine[r.SourceID] {
				dir, other = "in", r.SourceID
			}
			if mine[other] {
				continue
			}
			oe, ok := ix.set.EntityByID(other)
			if !ok {
				continue
			}
			out = append(out, model.ArticleRelation{
				Type: r.Type, Other: oe.Name, Direction: dir,
				OtherDescriptor: descriptor(oe, canon(oe.Type)),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Other != b.Other {
			return a.Other < b.Other
		}
		return a.Direction < b.Direction
	})
	if len(out) > maxRelations {
		out = out[:maxRelations]
	}
	return out
}

// linkableNames returns the names and aliases of the qualifying entities and
// topics other than the subject, deduplicated and sorted.
func linkableNames(ix *index, sub Subject, cands []Candidate) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, c := range cands {
		if c.ID == sub.ID {
			continue
		}
		add(c.Name)
		for _, a := range ix.aliases[c.ID] {
			add(a)
		}
	}
	sort.Strings(out)
	return out
}
