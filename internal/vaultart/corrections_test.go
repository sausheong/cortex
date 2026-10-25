package vaultart_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/testutil"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

func (u *wuser) liveMemory(id string) (cortex.Memory, bool) {
	u.e.t.Helper()
	ms, _, err := u.cx.ListMemories(bg, cortex.MemoryListFilter{ID: id}, 1, 0)
	if err != nil {
		u.e.t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].ExpiredAt != nil || ms[0].InvalidAt != nil {
		return cortex.Memory{}, false
	}
	return ms[0], true
}

func (u *wuser) openReady(sub vaultart.Subject) {
	u.e.t.Helper()
	u.e.w.Open(bg, u.user, sub, false)
	u.e.w.Wait()
}

func TestCorrectionIsUserMemoryLinkedToSubject(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	id, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "  Ada moved to Lisbon.  ")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := e.owner.liveMemory(id)
	if !ok {
		t.Fatal("correction not live")
	}
	if m.Content != "Ada moved to Lisbon." || m.Speaker != "user" || m.Source != model.SourceVaultCorrection ||
		m.Confidence != 1 || m.WrittenBy != "" || len(m.EntityIDs) != 1 || m.EntityIDs[0] != sub.ID {
		t.Fatalf("unexpected memory: %+v", m)
	}
}

func TestCorrectionMakesArticleStaleAndNextOpenRewrites(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	e.owner.openReady(sub)
	if e.llm.Calls != 1 || e.owner.article(sub).Stale {
		t.Fatalf("setup: calls %d", e.llm.Calls)
	}
	if _, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada prefers tea."); err != nil {
		t.Fatal(err)
	}
	if !e.owner.article(sub).Stale {
		t.Fatal("article not stale")
	}
	if res := e.w.Open(bg, e.owner.user, sub, false); res.State != vaultart.Writing {
		t.Fatalf("state %v", res.State)
	}
	e.w.Wait()
	if e.llm.Calls != 2 || e.owner.article(sub).Stale {
		t.Fatalf("not rewritten: calls %d", e.llm.Calls)
	}
}

func TestCorrectionTextReachesPrompt(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	if _, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada actually lives in Porto."); err != nil {
		t.Fatal(err)
	}
	e.owner.openReady(sub)
	found := false
	for _, m := range e.llm.LastInput.Memories {
		if strings.Contains(m.Content, "lives in Porto") && m.Correction {
			found = true
		}
	}
	if !found {
		t.Fatalf("correction missing from prompt: %+v", e.llm.LastInput.Memories)
	}
}

func TestCorrectionPinnedInPromptBeyondBudget(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 0)
	pad := strings.Repeat("filler words ", 10)
	for i := 0; i < 500; i++ {
		e.owner.addMem(sub.ID, fmt.Sprintf("%s %d", pad, i))
	}
	if _, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Pinned correction text."); err != nil {
		t.Fatal(err)
	}
	e.owner.openReady(sub)
	in := e.llm.LastInput
	if len(in.Memories) >= 501 {
		t.Fatalf("budget not exceeded: %d memories", len(in.Memories))
	}
	if len(in.Memories) == 0 || !in.Memories[0].Correction || in.Memories[0].Content != "Pinned correction text." {
		t.Fatalf("correction not pinned first: %+v", in.Memories[0])
	}
}

func TestCorrectionNoExtractorCall(t *testing.T) {
	e := newWenv(t, nil)
	var calls atomic.Int32
	e.root.SetExtractor(&testutil.MockExtractor{ExtractFn: func(ctx context.Context, content, contentType string) (*cortex.Extraction, error) {
		calls.Add(1)
		return &cortex.Extraction{}, nil
	}})
	sub := e.owner.subject("Ada", 3)
	if _, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada is left-handed."); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("extractor called %d times", calls.Load())
	}
}

func TestCorrectionEmbedded(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	id, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada is left-handed.")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.root.DB().QueryRow(`SELECT count(*) FROM embeddings WHERE ref_id = $1 AND ref_type = 'memory'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("embeddings: %d", n)
	}
}

func topicSubject(t *testing.T, u *wuser) (vaultart.Subject, string, string) {
	t.Helper()
	a := u.subject("Tea", 4)
	b := u.subject("Coffee", 1)
	return vaultart.Subject{ID: "topic-x", Group: vaultart.GroupTopics, Kind: "topic", Name: "Tea", Members: []string{a.ID, b.ID}}, a.ID, b.ID
}

func TestCorrectionForTopicLinksStrongestMemberAndUndoByAnyMember(t *testing.T) {
	e := newWenv(t, nil)
	sub, strongest, other := topicSubject(t, e.owner)
	id, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Both are drinks.")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := e.owner.liveMemory(id)
	if !ok || len(m.EntityIDs) != 1 || m.EntityIDs[0] != strongest {
		t.Fatalf("not linked to strongest: %+v", m)
	}
	// A correction linked to the other member can be undone through the topic.
	om := &cortex.Memory{Content: "Coffee note.", Speaker: "user", Source: model.SourceVaultCorrection, Confidence: 1, EntityIDs: []string{other}}
	if err := e.owner.cx.PutMemory(bg, om); err != nil {
		t.Fatal(err)
	}
	workset.Invalidate(e.owner.id)
	if got := vaultart.ListCorrections(mustSet(t, e.owner), sub); len(got) != 2 {
		t.Fatalf("corrections: %d", len(got))
	}
	if err := vaultart.UndoCorrection(bg, e.owner.cx, sub, om.ID); err != nil {
		t.Fatal(err)
	}
	if err := vaultart.UndoCorrection(bg, e.owner.cx, sub, id); err != nil {
		t.Fatal(err)
	}
}

func mustSet(t *testing.T, u *wuser) *workset.Set {
	t.Helper()
	s, err := workset.For(bg, u.h)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCorrectionDoubleSubmitSavesOnceAcrossRestart(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	id1, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada likes tea.")
	if err != nil {
		t.Fatal(err)
	}
	// A restart: no working set, a fresh Writer.
	workset.Reset()
	e.w = vaultart.NewWriter(vaultart.Deps{Root: e.root, Now: e.clk.Now, KeyTTL: time.Minute})
	id2, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "  Ada likes tea. ")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("saved twice: %s %s", id1, id2)
	}
	if got := vaultart.ListCorrections(mustSet(t, e.owner), sub); len(got) != 1 {
		t.Fatalf("corrections: %d", len(got))
	}
	// Different text is a different correction.
	id3, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada likes coffee.")
	if err != nil || id3 == id1 {
		t.Fatalf("distinct text: %v %s", err, id3)
	}
}

func TestCorrectionUndoSoftDeletes(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	e.owner.openReady(sub)
	id, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada likes tea.")
	if err != nil {
		t.Fatal(err)
	}
	e.owner.openReady(sub) // rewrite clears stale
	if e.owner.article(sub).Stale {
		t.Fatal("setup: still stale")
	}
	if err := vaultart.UndoCorrection(bg, e.owner.cx, sub, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.owner.liveMemory(id); ok {
		t.Fatal("still live")
	}
	del, err := e.owner.cx.ListDeleted(bg, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range del {
		found = found || m.ID == id
	}
	if !found {
		t.Fatal("not in Deleted")
	}
	if !e.owner.article(sub).Stale {
		t.Fatal("article not stale after undo")
	}
}

func TestCorrectionUndoRefusesForeignMemory(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	other := e.otherUser("bob")
	osub := other.subject("Bob", 3)
	foreign, err := vaultart.AddCorrection(bg, other.cx, osub, "Bob likes tea.")
	if err != nil {
		t.Fatal(err)
	}
	plain := e.owner.addMem(sub.ID, "Plain note")
	elsewhere := e.owner.subject("Eve", 3)
	eveCorr, err := vaultart.AddCorrection(bg, e.owner.cx, elsewhere, "Eve likes tea.")
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"foreign user": foreign, "not a correction": plain, "other subject": eveCorr, "unknown": "nope"} {
		if err := vaultart.UndoCorrection(bg, e.owner.cx, sub, id); err == nil {
			t.Errorf("%s: undo allowed", name)
		}
	}
	if _, ok := other.liveMemory(foreign); !ok {
		t.Error("foreign memory was deleted")
	}
	if _, ok := e.owner.liveMemory(eveCorr); !ok {
		t.Error("other subject's correction was deleted")
	}
}

func TestCorrectionEmptyAndTooLong(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 3)
	for _, s := range []string{"", "   \n\t ", strings.Repeat("x", 2001)} {
		if _, err := vaultart.AddCorrection(bg, e.owner.cx, sub, s); err != vaultart.ErrBadCorrection {
			t.Errorf("%d chars: %v", len(s), err)
		}
	}
	if _, err := vaultart.AddCorrection(bg, e.owner.cx, sub, strings.Repeat("x", 2000)); err != nil {
		t.Fatalf("2000 chars: %v", err)
	}
}

type reconcilingLLM struct {
	*testutil.MockLLM
	pair model.ConflictPair
}

func (r *reconcilingLLM) DetectConflicts(ctx context.Context, ms []model.Memory) ([]model.ConflictPair, error) {
	return []model.ConflictPair{r.pair}, nil
}

func TestReconcileMaySupersedeWrongMemory(t *testing.T) {
	e := newWenv(t, nil)
	sub := e.owner.subject("Ada", 1)
	wrong := e.owner.addMem(sub.ID, "Ada lives in Madrid.")
	time.Sleep(5 * time.Millisecond)
	corr, err := vaultart.AddCorrection(bg, e.owner.cx, sub, "Ada lives in Lisbon.")
	if err != nil {
		t.Fatal(err)
	}
	e.root.SetLLM(&reconcilingLLM{MockLLM: e.llm, pair: model.ConflictPair{StaleID: wrong, SupersededByID: corr, Reason: "moved"}})
	if _, err := e.owner.cx.ApplyReconcile(bg); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.owner.liveMemory(wrong); ok {
		t.Fatal("wrong memory still live")
	}
	if _, ok := e.owner.liveMemory(corr); !ok {
		t.Fatal("correction retired")
	}
	if err := vaultart.UndoCorrection(bg, e.owner.cx, sub, corr); err != nil {
		t.Fatalf("undo after reconcile: %v", err)
	}
	if _, ok := e.owner.liveMemory(corr); ok {
		t.Fatal("correction still live after undo")
	}
}
