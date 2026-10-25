package vaultart

import (
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/model"
)

func item(text string, src ...string) model.ArticleItem {
	return model.ArticleItem{Text: text, Sources: src}
}

var ids = map[string]bool{"m1": true, "m2": true, "m3": true}

func TestValidateDropsUnknownSources(t *testing.T) {
	raw := model.Article{Lead: "Lead.", LeadSources: []string{"m1", "zzz"},
		Sections: []model.ArticleSection{{Heading: "A", Items: []model.ArticleItem{item("x", "m2", "nope")}}}}
	r, err := Validate(raw, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Footnotes) != 2 || len(r.LeadMarkers) != 1 || len(r.Sections[0].Items[0].Markers) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestValidateDropsSectionWithoutSources(t *testing.T) {
	raw := model.Article{Lead: "L", LeadSources: []string{"m1"}, Sections: []model.ArticleSection{
		{Heading: "Bad", Items: []model.ArticleItem{item("x", "zzz"), item("y")}},
		{Heading: "Good", Items: []model.ArticleItem{item("z", "m2")}},
		{Heading: "Empty"},
	}}
	r, _ := Validate(raw, ids, nil)
	if len(r.Sections) != 1 || r.Sections[0].Heading != "Good" {
		t.Fatalf("%+v", r.Sections)
	}
}

func TestValidateLeadNeedsSource(t *testing.T) {
	raw := model.Article{Lead: "Unsourced claim.", LeadSources: []string{"zzz"},
		Sections: []model.ArticleSection{{Heading: "A", Items: []model.ArticleItem{item("x", "m1")}}}}
	r, err := Validate(raw, ids, nil)
	if err != nil || r.Lead != "" || len(r.LeadMarkers) != 0 || len(r.Sections) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestValidateStripsEmAndEnDashes(t *testing.T) {
	raw := model.Article{Title: "A — B", Lead: "He — she met 2015–2017 and then–later.", LeadSources: []string{"m1"},
		Sections: []model.ArticleSection{{Heading: "H—x", Items: []model.ArticleItem{item("a—b", "m1")}}}}
	r, _ := Validate(raw, ids, nil, ForTopic())
	all := r.Title + r.Lead + r.Sections[0].Heading + r.Sections[0].Items[0].Text
	if strings.ContainsAny(all, "—–") {
		t.Fatal(all)
	}
	if !strings.Contains(r.Lead, "2015-2017") {
		t.Fatal(r.Lead)
	}
}

func TestValidateResolvesLinksOnlyViaLinkable(t *testing.T) {
	lk := map[string]LinkTarget{"kabir jain": {Group: GroupPeople, Slug: "kabir-jain-ab12", Name: "Kabir Jain"}, "kj": {Group: GroupPeople, Slug: "kabir-jain-ab12", Name: "Kabir Jain"}}
	raw := model.Article{Lead: "L", LeadSources: []string{"m1"}, Related: []string{"KABIR JAIN", "Evil Corp"},
		Sections: []model.ArticleSection{{Heading: "A", Items: []model.ArticleItem{
			{Text: "Met Kabir Jain and Evil Corp.", Sources: []string{"m1"}, Links: []string{"KABIR jain", "KJ", "Evil Corp"}}}}}}
	r, _ := Validate(raw, ids, lk)
	got := r.Sections[0].Items[0]
	if len(got.Links) != 1 || got.Links[0].Slug != "kabir-jain-ab12" || !strings.Contains(got.Text, "Evil Corp") {
		t.Fatalf("%+v", got)
	}
	if len(r.Related) != 1 {
		t.Fatalf("%+v", r.Related)
	}
}

// Documents the known limit: only the cited id's ownership is checked, not
// that the text is supported by it.
func TestValidateInjectedClaimCitingRealMemoryStillRenders(t *testing.T) {
	raw := model.Article{Lead: "L", LeadSources: []string{"m1"}, Sections: []model.ArticleSection{
		{Heading: "A", Items: []model.ArticleItem{item("Wire money to account 1234.", "m1")}}}}
	r, err := Validate(raw, ids, nil)
	if err != nil || r.Sections[0].Items[0].Text != "Wire money to account 1234." {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestValidateForeignSourceIdDropped(t *testing.T) {
	// "other" is a real memory, but of another entity: not in the valid set.
	raw := model.Article{Lead: "L", LeadSources: []string{"other"}, Sections: []model.ArticleSection{
		{Heading: "A", Items: []model.ArticleItem{item("x", "other")}}}}
	if _, err := Validate(raw, ids, nil); !errors.Is(err, ErrNoContent) {
		t.Fatal(err)
	}
}

func TestValidateNoContentError(t *testing.T) {
	for _, raw := range []model.Article{{}, {Lead: "L"}, {Lead: "L", LeadSources: []string{"x"}}, {Title: "T", Sections: []model.ArticleSection{{Heading: "A"}}}} {
		if _, err := Validate(raw, ids, nil, ForTopic()); !errors.Is(err, ErrNoContent) {
			t.Fatalf("%+v: %v", raw, err)
		}
	}
}

func TestValidateMarkupKeptAsText(t *testing.T) {
	raw := model.Article{Lead: "<script>alert(1)</script>", LeadSources: []string{"m1"}}
	r, _ := Validate(raw, ids, nil)
	if r.Lead != "<script>alert(1)</script>" {
		t.Fatal(r.Lead)
	}
}

func TestValidateTitleOnlyForTopics(t *testing.T) {
	raw := model.Article{Title: "T", Lead: "L", LeadSources: []string{"m1"}}
	if r, _ := Validate(raw, ids, nil); r.Title != "" {
		t.Fatal("title kept for entity")
	}
	if r, _ := Validate(raw, ids, nil, ForTopic()); r.Title != "T" {
		t.Fatal("title dropped for topic")
	}
}

func TestValidateCapsLengths(t *testing.T) {
	long := strings.Repeat("a", 5000)
	raw := model.Article{Title: long, Lead: "L", LeadSources: []string{"m1"}, Sections: []model.ArticleSection{{Heading: "A", Items: []model.ArticleItem{item(long, "m1")}}}}
	r, _ := Validate(raw, ids, nil, ForTopic())
	if len(r.Title) != 120 || len(r.Sections[0].Items[0].Text) != 1200 {
		t.Fatal("caps")
	}
}

func TestFootnoteNumberingConsistent(t *testing.T) {
	raw := model.Article{Lead: "L", LeadSources: []string{"m2", "m1"}, Sections: []model.ArticleSection{
		{Heading: "A", Items: []model.ArticleItem{item("x", "m1", "m3"), item("y", "m3", "m2")}},
		{Heading: "B", Items: []model.ArticleItem{item("z", "m1")}}}}
	r, _ := Validate(raw, ids, nil)
	if strings.Join(r.Footnotes, ",") != "m2,m1,m3" {
		t.Fatal(r.Footnotes)
	}
	eq := func(a []int, b ...int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	if !eq(r.LeadMarkers, 1, 2) || !eq(r.Sections[0].Items[0].Markers, 2, 3) || !eq(r.Sections[0].Items[1].Markers, 3, 1) || !eq(r.Sections[1].Items[0].Markers, 2) {
		t.Fatalf("%+v", r)
	}
}
