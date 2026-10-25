package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

func sampleArticle() model.Article {
	return model.Article{
		Title: "Zebulon Quasar", Lead: "SECRETLEAD about Zebulon",
		LeadSources: []string{"m1"},
		Sections: []model.ArticleSection{{Heading: "Work", Items: []model.ArticleItem{
			{Text: "SECRETITEM", Sources: []string{"m1"}, Links: []string{"Acme"}}}}},
	}
}

func putSample(t *testing.T, env *enctest.Env, subject string, fp []byte) store.ArticleRow {
	t.Helper()
	r := store.ArticleRow{SubjectID: subject, Kind: "entity", ShortID: "abcd" + subject,
		Slug: "zebulon-quasar-abcd1234", Fingerprint: fp, Tokens: 42, Article: sampleArticle()}
	if err := env.H.PutArticle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestArticleRoundTrip(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	r := putSample(t, env, "e1", []byte{1, 2, 3})
	got, err := env.H.GetArticle(ctx, "e1")
	if err != nil || got == nil {
		t.Fatal(got, err)
	}
	if got.Slug != r.Slug || got.ShortID != r.ShortID || got.Tokens != 42 || got.Article.Title != "Zebulon Quasar" ||
		got.Article.Sections[0].Items[0].Text != "SECRETITEM" || string(got.Fingerprint) != "\x01\x02\x03" || got.Stale {
		t.Fatalf("got %+v", got)
	}
	if g, err := env.H.GetArticle(ctx, "nope"); g != nil || err != nil {
		t.Fatal(g, err)
	}
	if err := env.H.MarkArticleStale(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	meta, err := env.Keyless.ListArticleMeta(ctx)
	if err != nil || len(meta) != 1 || !meta[0].Stale || meta[0].ShortID != "abcde1" || meta[0].Slug != "" {
		t.Fatal(meta, err)
	}
	if err := env.H.DeleteArticles(ctx, "e1", "zz"); err != nil {
		t.Fatal(err)
	}
	if g, _ := env.H.GetArticle(ctx, "e1"); g != nil {
		t.Fatal("not deleted")
	}
	if _, err := env.Keyless.GetArticle(ctx, "e1"); !errors.Is(err, model.ErrLocked) {
		t.Fatal(err)
	}
	if err := env.Keyless.PutArticle(ctx, r); !errors.Is(err, model.ErrLocked) {
		t.Fatal(err)
	}
}

func TestArticleCiphertextHasNoPlaintext(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	putSample(t, env, "e1", []byte{9})
	var raw string
	env.DB.QueryRow(`SELECT to_jsonb(t)::text FROM vault_articles t WHERE user_id = $1`, env.UserID).Scan(&raw)
	for _, s := range []string{"SECRET", "Zebulon", "zebulon", "Acme"} {
		if strings.Contains(raw, s) {
			t.Fatalf("leaks %q: %s", s, raw)
		}
	}
}

func TestArticleOpenWithWrongKeyFails(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	putSample(t, env, "e1", []byte{9})
	uk2, _ := keys.NewUK()
	defer uk2.Zero()
	h2 := env.HandleFor(env.UserID, "encrypted", uk2)
	if _, err := h2.GetArticle(context.Background(), "e1"); !errors.Is(err, store.ErrArticleCorrupt) {
		t.Fatalf("got %v", err)
	}
}

func TestArticleAADBoundToRowAndFingerprint(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	putSample(t, env, "e1", []byte{1})
	putSample(t, env, "e2", []byte{1})
	// copy e1's article ciphertext to e2
	env.Exec(`UPDATE vault_articles SET article_ct = (SELECT article_ct FROM vault_articles WHERE user_id=$1 AND subject_id='e1')
		WHERE user_id=$1 AND subject_id='e2'`, env.UserID)
	if _, err := env.H.GetArticle(ctx, "e2"); !errors.Is(err, store.ErrArticleCorrupt) {
		t.Fatalf("cross-subject copy opened: %v", err)
	}
	// old ciphertext under a new fingerprint
	env.Exec(`UPDATE vault_articles SET fingerprint = '\x02' WHERE user_id=$1 AND subject_id='e1'`, env.UserID)
	if _, err := env.H.GetArticle(ctx, "e1"); !errors.Is(err, store.ErrArticleCorrupt) {
		t.Fatalf("replay under new fingerprint opened: %v", err)
	}
}

func TestCorruptArticleReturnsErrArticleCorrupt(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	putSample(t, env, "e1", []byte{1})
	env.Exec(`UPDATE vault_articles SET article_ct = '\x0102' WHERE user_id=$1`, env.UserID)
	if _, err := env.H.GetArticle(context.Background(), "e1"); !errors.Is(err, store.ErrArticleCorrupt) {
		t.Fatal(err)
	}
	env.Exec(`UPDATE vault_articles SET article_ct = NULL WHERE user_id=$1`, env.UserID)
	if _, err := env.H.GetArticle(context.Background(), "e1"); !errors.Is(err, store.ErrArticleCorrupt) {
		t.Fatal(err)
	}
}

func TestNoPlaintextNamesInAnyVaultColumn(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	putSample(t, env, "e1", []byte{1})
	if err := env.H.PutTopics(ctx, []store.TopicRow{{TopicID: "t1", ShortID: "tttt1111", Slug: "zebulon-cluster-tttt1111",
		Title: "Zebulon Cluster", WrittenTitle: "Zebulon Story", Members: []string{"e1", "e2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := env.H.PutVaultSettings(ctx, map[string]any{"note": "Zebulon"}); err != nil {
		t.Fatal(err)
	}
	if err := env.H.RecordWrite(ctx, 5, true); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"vault_articles", "vault_topics", "vault_settings", "vault_writes"} {
		var raw string
		if err := env.DB.QueryRow(fmt.Sprintf(`SELECT coalesce(string_agg(to_jsonb(t)::text, ' '), '') FROM %s t`, tbl)).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw == "" {
			t.Fatalf("%s empty", tbl)
		}
		low := strings.ToLower(raw)
		for _, s := range []string{"zebulon", "quasar", "cluster", "secret"} {
			if strings.Contains(low, s) {
				t.Fatalf("%s leaks %q: %s", tbl, s, raw)
			}
		}
	}
}

func TestPutTopicsDiffApplyKeepsRows(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	a := store.TopicRow{TopicID: "t1", ShortID: "s1", Slug: "a", Title: "A", Members: []string{"e1", "e2"}}
	b := store.TopicRow{TopicID: "t2", ShortID: "s2", Slug: "b", Title: "B", Members: []string{"e3", "e4", "e5"}}
	if err := env.H.PutTopics(ctx, []store.TopicRow{a, b}); err != nil {
		t.Fatal(err)
	}
	var xminBefore string
	env.DB.QueryRow(`SELECT ctid::text FROM vault_topics WHERE topic_id='t1'`).Scan(&xminBefore)
	var created1 string
	env.DB.QueryRow(`SELECT created_at::text FROM vault_topics WHERE topic_id='t1'`).Scan(&created1)
	a.Members = []string{"e1", "e2", "e6"}
	a.WrittenTitle = "Written"
	if err := env.H.PutTopics(ctx, []store.TopicRow{a}); err != nil {
		t.Fatal(err)
	}
	var created2 string
	env.DB.QueryRow(`SELECT created_at::text FROM vault_topics WHERE topic_id='t1'`).Scan(&created2)
	if created1 != created2 {
		t.Fatal("t1 was recreated")
	}
	got, err := env.H.GetTopics(ctx)
	if err != nil || len(got) != 1 || got[0].TopicID != "t1" || len(got[0].Members) != 3 || got[0].WrittenTitle != "Written" {
		t.Fatal(got, err)
	}
	var mc int
	env.DB.QueryRow(`SELECT member_count FROM vault_topics WHERE topic_id='t1'`).Scan(&mc)
	if mc != 3 {
		t.Fatalf("member_count %d", mc)
	}
	if err := env.H.SetTopicWrittenTitle(ctx, "t1", "Again"); err != nil {
		t.Fatal(err)
	}
	got, _ = env.H.GetTopics(ctx)
	if got[0].WrittenTitle != "Again" {
		t.Fatal(got)
	}
	if _, err := env.Keyless.GetTopics(ctx); !errors.Is(err, model.ErrLocked) {
		t.Fatal(err)
	}
}

func TestVaultSettingsRoundTripAndKeyless(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	type S struct{ Threshold int }
	var s S
	if err := env.H.GetVaultSettings(ctx, &s); !errors.Is(err, model.ErrNotFound) {
		t.Fatal(err)
	}
	if err := env.H.PutVaultSettings(ctx, S{7}); err != nil {
		t.Fatal(err)
	}
	if err := env.H.PutVaultSettings(ctx, S{9}); err != nil {
		t.Fatal(err)
	}
	if err := env.H.GetVaultSettings(ctx, &s); err != nil || s.Threshold != 9 {
		t.Fatal(s, err)
	}
	if err := env.Keyless.GetVaultSettings(ctx, &s); !errors.Is(err, model.ErrLocked) {
		t.Fatal(err)
	}
	if err := env.Keyless.PutVaultSettings(ctx, S{1}); !errors.Is(err, model.ErrLocked) {
		t.Fatal(err)
	}
}

func TestRecordWriteKeepsLast50(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		if err := env.H.RecordWrite(ctx, i, i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	got, err := env.Keyless.RecentWrites(ctx, 100)
	if err != nil || len(got) != 50 {
		t.Fatal(len(got), err)
	}
	if got[0].Tokens != 59 || got[49].Tokens != 10 {
		t.Fatalf("order/prune wrong: %d %d", got[0].Tokens, got[49].Tokens)
	}
	if g, _ := env.H.RecentWrites(ctx, 3); len(g) != 3 {
		t.Fatal(len(g))
	}
}

func TestVaultTablesCascadeOnUserDelete(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	putSample(t, env, "e1", []byte{1})
	env.H.PutTopics(ctx, []store.TopicRow{{TopicID: "t1", ShortID: "s1", Members: []string{"a"}}})
	env.H.PutVaultSettings(ctx, map[string]int{"a": 1})
	env.H.RecordWrite(ctx, 1, true)
	env.Exec(`DELETE FROM users WHERE id = $1`, env.UserID)
	for _, tbl := range []string{"vault_articles", "vault_topics", "vault_settings", "vault_writes"} {
		if n := env.Count(fmt.Sprintf(`SELECT count(*) FROM %s WHERE user_id = $1`, tbl), env.UserID); n != 0 {
			t.Errorf("%s has %d rows", tbl, n)
		}
	}
}
