package vaultart

import (
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/workset"
)

// Group is a section of Vault home.
type Group string

// The three Vault home sections.
const (
	GroupPeople   Group = "people"
	GroupProjects Group = "projects"
	GroupTopics   Group = "topics"
)

// Candidate is an entity that qualifies for its own article.
type Candidate struct {
	ID, Name, Type, Descriptor string
	Group                      Group
	// Kind is the canonical entity type; "event" stays distinct from the
	// projects group so the index can split events out later.
	Kind             string
	Memories         int
	Updated          time.Time
	ConnectedToOwner bool
	IsOwner          bool
}

// canon maps a free-text entity type onto the curated set.
func canon(typ string) string {
	c, _ := ontology.CanonicalEntityType(typ)
	return c
}

// IsAccountLike reports identifiers (email addresses, phone numbers, handles)
// that never get articles.
func IsAccountLike(typ string) bool { return canon(typ) == ontology.TypeAccount }

// TopicEligible reports types folded into topic clusters instead of getting
// a standalone article.
func TopicEligible(typ string) bool {
	switch canon(typ) {
	case ontology.TypeConcept, ontology.TypeDocument, ontology.TypeProduct,
		ontology.TypePlace, ontology.TypeGroup:
		return true
	}
	return false
}

// groupFor returns the group of an article-eligible type.
func groupFor(kind string) (Group, bool) {
	switch kind {
	case ontology.TypePerson:
		return GroupPeople, true
	case ontology.TypeOrganization, ontology.TypeProject, ontology.TypeEvent:
		return GroupProjects, true
	}
	return "", false
}

// LiveMemoryCounts counts live memories per entity in a single pass.
func LiveMemoryCounts(set *workset.Set) map[string]int {
	out := map[string]int{}
	for _, m := range set.MemoriesMode(model.TemporalMode{}) {
		seen := map[string]bool{}
		for _, id := range m.EntityIDs {
			if !seen[id] {
				seen[id] = true
				out[id]++
			}
		}
	}
	return out
}

func typeLabel(kind string) string {
	switch kind {
	case ontology.TypePerson:
		return "Person"
	case ontology.TypeOrganization:
		return "Organisation"
	case ontology.TypeProject:
		return "Project"
	case ontology.TypeEvent:
		return "Event"
	}
	return "Topic"
}

var dashes = strings.NewReplacer("\u2014", "-", "\u2013", "-")

// descriptor is the entity's own role, description or title when it has one,
// else the type label. It is never model-written, so it carries no claim the
// user's data does not.
func descriptor(e model.Entity, kind string) string {
	for _, k := range []string{"role", "description", "title"} {
		if v, ok := e.Attributes[k].(string); ok {
			v = strings.Join(strings.Fields(dashes.Replace(v)), " ")
			if v == "" {
				continue
			}
			if r := []rune(v); len(r) > 140 {
				v = strings.TrimSpace(string(r[:140]))
			}
			return v
		}
	}
	return typeLabel(kind)
}

// Qualify returns the entities that earn a standalone article: people,
// organisations, projects and events with at least s.Threshold live
// memories, or (when s.IncludeConnected) at least one live memory and a
// current relationship to the owner. The owner entity always qualifies.
// Account-like entities and concept-like types never do; those are folded
// into topics. Order is memories descending, then name, then id.
func Qualify(set *workset.Set, ownerID string, s Settings, now time.Time) []Candidate {
	s = s.Normalize()
	counts := LiveMemoryCounts(set)
	connected := map[string]bool{}
	if s.IncludeConnected && ownerID != "" {
		for _, r := range set.RelationshipsFiltered(false, nil, now) {
			if r.SourceID == ownerID {
				connected[r.TargetID] = true
			} else if r.TargetID == ownerID {
				connected[r.SourceID] = true
			}
		}
	}
	var out []Candidate
	for _, e := range set.Entities() {
		isOwner := ownerID != "" && e.ID == ownerID
		kind := canon(e.Type)
		g, ok := groupFor(kind)
		n := counts[e.ID]
		conn := connected[e.ID] && n >= 1
		switch {
		case isOwner:
			if !ok {
				g = GroupPeople
			}
		case !ok:
			continue
		case n < s.Threshold && !conn:
			continue
		}
		d := descriptor(e, kind)
		if isOwner {
			d = "You"
		}
		out = append(out, Candidate{
			ID: e.ID, Name: e.Name, Type: e.Type, Descriptor: d, Group: g, Kind: kind,
			Memories: n, Updated: e.UpdatedAt,
			ConnectedToOwner: connected[e.ID], IsOwner: isOwner,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Memories != out[j].Memories {
			return out[i].Memories > out[j].Memories
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}
