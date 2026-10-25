package vaultart_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

func (u *wuser) export() map[string]string {
	u.e.t.Helper()
	set, err := workset.For(bg, u.h)
	if err != nil {
		u.e.t.Fatal(err)
	}
	files, err := vaultart.BuildExport(bg, u.h, set, "", vaultart.DefaultSettings(), u.e.clk.Now())
	if err != nil {
		u.e.t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = f.Content
	}
	return out
}

func (u *wuser) write(sub vaultart.Subject) {
	u.e.t.Helper()
	u.e.w.Open(bg, u.user, sub, false)
	u.e.w.Wait()
}

func expPath(sub vaultart.Subject) string {
	return strings.TrimPrefix(vaultart.Path(sub.Group, vaultart.ArticleSlug(sub.Name, sub.ID)), "/vault/") + ".md"
}

func TestExportWrittenArticleWikilinksAndFootnotes(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada", 3)
	bob := u.subject("Bob", 3)
	if err := graph.New(u.h).PutRelationship(bg, &model.Relationship{SourceID: ada.ID, TargetID: bob.ID, Type: "works_with"}); err != nil {
		t.Fatal(err)
	}
	workset.Invalidate(u.id)
	var adaMems []string
	e.llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		for _, m := range in.Memories {
			adaMems = append(adaMems, m.ID)
		}
		return cortex.Article{
			Lead:        "Ada works with Bob.",
			LeadSources: []string{in.Memories[0].ID},
			Sections: []cortex.ArticleSection{{Heading: "Work", Items: []cortex.ArticleItem{
				{Text: "Ada builds things with Bob.", Sources: []string{in.Memories[1].ID}, Links: []string{"Bob"}},
				{Text: "A bullet.", Bullet: true, Sources: []string{in.Memories[0].ID}},
			}}},
		}, cortex.ArticleUsage{}, nil
	}
	u.write(ada)
	files := u.export()
	md := files[expPath(ada)]
	if md == "" {
		t.Fatalf("no file for ada: %v", fileNames(files))
	}
	link := "[[" + strings.TrimSuffix(expPath(bob), ".md") + "|Bob]]"
	if !strings.Contains(md, link) {
		t.Fatalf("missing wikilink %s in:\n%s", link, md)
	}
	if !strings.HasPrefix(md, "---\n") || !strings.Contains(md, "# Ada\n") {
		t.Fatalf("frontmatter/title:\n%s", md)
	}
	if !strings.Contains(md, "[^1]") || !strings.Contains(md, "[^1]: ") || !strings.Contains(md, "[^2]: ") {
		t.Fatalf("footnotes missing:\n%s", md)
	}
	if !strings.Contains(md, "## Related") || strings.Contains(md, "<") {
		t.Fatalf("related/html:\n%s", md)
	}
	if !regexp.MustCompile(`\[\^1\]: Ada fact \d \(\d{4}-\d{2}-\d{2}\)`).MatchString(md) {
		t.Fatalf("footnote text/date:\n%s", md)
	}
}

func TestExportStubForUnwrittenWithNote(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada", 3)
	md := u.export()[expPath(ada)]
	if !strings.Contains(md, vaultart.StubNote) || !strings.Contains(md, "Ada fact 0") || !strings.Contains(md, "Ada fact 2") {
		t.Fatalf("stub:\n%s", md)
	}
	if !strings.Contains(md, "This article has not been written yet. Open it in Cortex to write it.") {
		t.Fatal("note text")
	}
}

func TestExportIndexListsAllQualifying(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada", 3)
	bob := u.subject("Bob", 3)
	u.subject("Tiny", 1) // does not qualify
	u.write(ada)
	files := u.export()
	ix := files["index.md"]
	for _, s := range []vaultart.Subject{ada, bob} {
		if !strings.Contains(ix, "[["+strings.TrimSuffix(expPath(s), ".md")+"|") {
			t.Fatalf("index misses %s:\n%s", s.Name, ix)
		}
	}
	if strings.Contains(ix, "Tiny") || files[expPath(vaultart.Subject{ID: "x", Group: vaultart.GroupPeople, Name: "Tiny"})] != "" {
		t.Fatal("non-qualifying listed")
	}
	if !strings.Contains(ix, "## People") || len(files) != 3 {
		t.Fatalf("files %v\n%s", fileNames(files), ix)
	}
}

func TestExportNeverCallsLLM(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada", 3)
	u.subject("Bob", 3)
	u.write(ada)
	before := e.llm.Calls
	u.addMem(ada.ID, "new fact makes ada stale")
	u.export()
	u.export()
	if e.llm.Calls != before {
		t.Fatalf("calls %d -> %d", before, e.llm.Calls)
	}
}

func TestExportEscapesForgedWikilinks(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada", 3)
	u.addMem(ada.ID, "see [[people/evil-12345678|Admin]] and [^9] and `code` <script>alert(1)</script>")
	u.addMem(ada.ID, "# heading\nsecond line")
	md := u.export()[expPath(ada)]
	if strings.Contains(md, "[[") || strings.Contains(md, "<script") || strings.Contains(md, "[^9]") {
		t.Fatalf("forged markup survived:\n%s", md)
	}
	if strings.Contains(md, "\n# heading") || strings.Contains(md, "\nsecond line") {
		t.Fatalf("newline not flattened:\n%s", md)
	}
	// A written article's footnote text is escaped too.
	e.llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		var ids []string
		for _, m := range in.Memories {
			ids = append(ids, m.ID)
		}
		return cortex.Article{Lead: "Lead text.", LeadSources: ids}, cortex.ArticleUsage{}, nil
	}
	u.write(ada)
	md = u.export()[expPath(ada)]
	if strings.Contains(md, "[[") || strings.Contains(md, "<script") || strings.Contains(md, "[^9]") {
		t.Fatalf("forged markup survived in footnotes:\n%s", md)
	}
}

func TestExportFrontmatterEscapesHostileTitle(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	hostile := "Evil\"\n---\nadmin: true\n\\: x"
	ada := u.subject(hostile, 3)
	files := u.export()
	var md string
	for p, c := range files {
		if p != "index.md" {
			md = c
		}
	}
	if md == "" {
		t.Fatalf("no file; ada=%+v keys=%v", ada, fileNames(files))
	}
	end := strings.Index(md[4:], "\n---\n")
	if end < 0 {
		t.Fatalf("no frontmatter end:\n%s", md)
	}
	fm := md[4 : 4+end]
	for _, line := range strings.Split(fm, "\n") {
		if !regexp.MustCompile(`^[a-z]+: ".*"$`).MatchString(line) {
			t.Fatalf("frontmatter line not a quoted string: %q\n%s", line, fm)
		}
	}
	if strings.Contains(fm, "admin: true") && !strings.Contains(fm, `\n`) {
		t.Fatalf("title not escaped:\n%s", fm)
	}
	if got := strings.Count(md, "\n---\n"); got != 1 {
		t.Fatalf("block markers: %d\n%s", got, md)
	}
}

func TestExportUserDataEmDashVerbatimProseClean(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada \u2014 Lovelace", 2)
	u.addMem(ada.ID, "She said \u2014 truly \u2014 hello")
	e.llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		return cortex.Article{
			Lead:        "A pioneer \u2014 the first.",
			LeadSources: []string{in.Memories[0].ID},
			Sections: []cortex.ArticleSection{{Heading: "Life \u2014 work", Items: []cortex.ArticleItem{
				{Text: "Wrote notes \u2014 many.", Sources: []string{in.Memories[0].ID}},
			}}},
		}, cortex.ArticleUsage{}, nil
	}
	u.write(ada)
	md := u.export()[expPath(ada)]
	if !strings.Contains(md, "# Ada \u2014 Lovelace") {
		t.Fatalf("name not verbatim:\n%s", md)
	}
	if !strings.Contains(md, "She said \u2014 truly \u2014 hello") {
		t.Fatalf("memory not verbatim:\n%s", md)
	}
	for _, bad := range []string{"A pioneer \u2014", "Life \u2014", "notes \u2014"} {
		if strings.Contains(md, bad) {
			t.Fatalf("prose kept an em dash %q:\n%s", bad, md)
		}
	}
	// Stub: cortex-written text has none.
	e2 := newWenv(t, nil)
	s := e2.owner.subject("Plain", 3)
	stub := e2.owner.export()[expPath(s)]
	fixed := strings.ReplaceAll(stub, "Plain fact", "")
	if strings.Contains(fixed, "\u2014") || strings.Contains(e2.owner.export()["index.md"], "\u2014") {
		t.Fatalf("cortex-written text has an em dash:\n%s", stub)
	}
}

func TestExportExcludesNonQualifyingCachedArticle(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	ada := u.subject("Ada", 3)
	u.write(ada)
	if _, ok := u.export()[expPath(ada)]; !ok {
		t.Fatal("ada should be exported first")
	}
	now := e.clk.Now()
	set, _ := workset.For(bg, u.h)
	for _, m := range set.MemoriesMode(model.TemporalMode{}) {
		if err := memory.New(u.h).InvalidateMemory(bg, m.ID, &now); err != nil {
			t.Fatal(err)
		}
	}
	workset.Invalidate(u.id)
	files := u.export()
	if _, ok := files[expPath(ada)]; ok {
		t.Fatalf("non-qualifying article exported: %v", fileNames(files))
	}
	if strings.Contains(files["index.md"], "Ada") {
		t.Fatal("index lists it")
	}
}

func fileNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

var _ = fmt.Sprint
