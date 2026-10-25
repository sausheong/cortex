package vaultart

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/workset"
)

// Fingerprint is the keyed digest that says whether an article is stale. It
// covers everything the writer sees that can change the article: every live
// memory of the subject (id and updated_at, not only those under the prompt
// budget), every relationship (id and invalid_at), the entity name, aliases
// and attributes, the article length, PromptVersion and the owner name.
//
// Linkable ids are deliberately excluded: including them would rewrite every
// article whenever any entity anywhere qualifies. Links are resolved at
// render time from the current index, so a newly qualifying name appears at
// the next natural rewrite. There is no unkeyed content hash; the digest is
// h.Blind of the canonical string, so it is nil without a key.
//
// ownerName is passed explicitly because the digest covers it.
func Fingerprint(h *store.Handle, set *workset.Set, sub Subject, ownerName string, s Settings) []byte {
	return buildIndex(set).fingerprint(h, sub, ownerName, s.Normalize())
}

// FingerprintAll computes the fingerprint of every subject in one pass over
// the set, keyed by Subject.ID. Each result equals Fingerprint for the same
// subject.
func FingerprintAll(h *store.Handle, set *workset.Set, subs []Subject, ownerName string, s Settings) map[string][]byte {
	s = s.Normalize()
	ix := buildIndex(set)
	out := make(map[string][]byte, len(subs))
	for _, sub := range subs {
		out[sub.ID] = ix.fingerprint(h, sub, ownerName, s)
	}
	return out
}

func (ix *index) fingerprint(h *store.Handle, sub Subject, ownerName string, s Settings) []byte {
	var b strings.Builder
	w := func(tag string, vals ...string) {
		b.WriteString(tag)
		for _, v := range vals {
			b.WriteByte(' ')
			b.WriteString(strconv.Quote(v))
		}
		b.WriteByte('\n')
	}
	w("prompt", PromptVersion)
	w("owner", ownerName)
	w("length", s.Length)
	w("subject", sub.Kind)
	if sub.Kind == "topic" {
		w("title", sub.Name)
	}

	ids := append([]string(nil), sub.entityIDs()...)
	sort.Strings(ids)
	for _, id := range ids {
		e, _ := ix.set.EntityByID(id)
		attrs, _ := json.Marshal(e.Attributes) // map keys marshal sorted
		w("entity", id, e.Name, string(attrs))
		for _, a := range ix.aliases[id] {
			w("alias", a)
		}
	}

	mems := ix.memories(sub) // sorted by id
	for _, m := range mems {
		w("mem", m.ID, strconv.FormatInt(m.UpdatedAt.UTC().UnixNano(), 10))
	}

	seen := map[string]bool{}
	var rels []string
	for _, id := range ids {
		for _, r := range ix.rels[id] {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			inv := "-"
			if r.InvalidAt != nil {
				inv = strconv.FormatInt(r.InvalidAt.UTC().UnixNano(), 10)
			}
			rels = append(rels, strconv.Quote(r.ID)+" "+strconv.Quote(inv))
		}
	}
	sort.Strings(rels)
	for _, r := range rels {
		b.WriteString("rel " + r + "\n")
	}
	return h.Blind(b.String())
}
