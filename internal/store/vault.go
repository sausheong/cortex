package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/model"
)

// Table names, used as the AAD table component.
const (
	tblVaultArticles = "vault_articles"
	tblVaultTopics   = "vault_topics"
	tblVaultSettings = "vault_settings"
)

// ErrArticleCorrupt means a stored article could not be opened or decoded;
// callers rewrite it instead of failing the request.
var ErrArticleCorrupt = errors.New("cortex: stored article is corrupt")

// ArticleRow is one stored article. Slug and Article are sealed at rest.
type ArticleRow struct {
	SubjectID   string
	Kind        string
	ShortID     string
	Slug        string
	Fingerprint []byte
	WrittenAt   time.Time
	Stale       bool
	Tokens      int
	Article     model.Article
}

// articleAAD binds the article ciphertext to its subject and fingerprint so an
// older ciphertext cannot be replayed under a newer fingerprint.
func articleAAD(subjectID string, fp []byte) string {
	return subjectID + "|" + hex.EncodeToString(fp)
}

// PutArticle upserts a sealed article.
func (h *Handle) PutArticle(ctx context.Context, r ArticleRow) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	art, err := h.SealJSON(tblVaultArticles, articleAAD(r.SubjectID, r.Fingerprint), "article", r.Article)
	if err != nil {
		return err
	}
	slug, err := h.SealStr(tblVaultArticles, r.SubjectID, "slug", r.Slug)
	if err != nil {
		return err
	}
	at := r.WrittenAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err = h.DB.ExecContext(ctx,
		`INSERT INTO vault_articles (user_id, subject_id, kind, short_id, slug_ct, fingerprint, written_at, stale, tokens, article_ct)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, subject_id) DO UPDATE SET kind = excluded.kind, short_id = excluded.short_id,
		   slug_ct = excluded.slug_ct, fingerprint = excluded.fingerprint, written_at = excluded.written_at,
		   stale = excluded.stale, tokens = excluded.tokens, article_ct = excluded.article_ct`,
		h.UserID, r.SubjectID, r.Kind, r.ShortID, slug, r.Fingerprint, at, r.Stale, r.Tokens, art)
	if err != nil {
		return fmt.Errorf("cortex: put article: %w", err)
	}
	return nil
}

// GetArticle returns the article for a subject, nil when absent. Any failure
// to open or decode it returns ErrArticleCorrupt.
func (h *Handle) GetArticle(ctx context.Context, subjectID string) (*ArticleRow, error) {
	if err := h.RequireKey(); err != nil {
		return nil, err
	}
	var r ArticleRow
	var slugCT, artCT []byte
	err := h.DB.QueryRowContext(ctx,
		`SELECT subject_id, kind, short_id, slug_ct, fingerprint, written_at, stale, tokens, article_ct
		 FROM vault_articles WHERE user_id = ? AND subject_id = ?`, h.UserID, subjectID).
		Scan(&r.SubjectID, &r.Kind, &r.ShortID, &slugCT, &r.Fingerprint, &r.WrittenAt, &r.Stale, &r.Tokens, &artCT)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get article: %w", err)
	}
	if err := h.OpenJSON(tblVaultArticles, articleAAD(r.SubjectID, r.Fingerprint), "article", artCT, &r.Article); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrArticleCorrupt, err)
	}
	if r.Slug, err = h.OpenOptStr(tblVaultArticles, r.SubjectID, "slug", slugCT); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrArticleCorrupt, err)
	}
	return &r, nil
}

// ListArticleMeta lists every article without decrypting anything. The rows
// carry no Slug and no Article.
func (h *Handle) ListArticleMeta(ctx context.Context) ([]ArticleRow, error) {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT subject_id, kind, short_id, fingerprint, written_at, stale, tokens
		 FROM vault_articles WHERE user_id = ? ORDER BY subject_id`, h.UserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: list articles: %w", err)
	}
	defer rows.Close()
	var out []ArticleRow
	for rows.Next() {
		var r ArticleRow
		if err := rows.Scan(&r.SubjectID, &r.Kind, &r.ShortID, &r.Fingerprint, &r.WrittenAt, &r.Stale, &r.Tokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListArticleSlugs opens only the sealed slug of every article, keyed by
// subject id. It never touches the article body. A slug that cannot be opened
// is left out so callers fall back to a derived one.
func (h *Handle) ListArticleSlugs(ctx context.Context) (map[string]string, error) {
	if err := h.RequireKey(); err != nil {
		return nil, err
	}
	rows, err := h.DB.QueryContext(ctx,
		`SELECT subject_id, slug_ct FROM vault_articles WHERE user_id = ?`, h.UserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: list article slugs: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id string
		var ct []byte
		if err := rows.Scan(&id, &ct); err != nil {
			return nil, err
		}
		if s, err := h.OpenOptStr(tblVaultArticles, id, "slug", ct); err == nil && s != "" {
			out[id] = s
		}
	}
	return out, rows.Err()
}

// MarkArticleStale flags an article as needing a rewrite.
func (h *Handle) MarkArticleStale(ctx context.Context, subjectID string) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	_, err := h.DB.ExecContext(ctx,
		`UPDATE vault_articles SET stale = true WHERE user_id = ? AND subject_id = ?`, h.UserID, subjectID)
	if err != nil {
		return fmt.Errorf("cortex: mark article stale: %w", err)
	}
	return nil
}

// DeleteArticles removes the articles of the given subjects.
func (h *Handle) DeleteArticles(ctx context.Context, ids ...string) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := h.DB.ExecContext(ctx,
			`DELETE FROM vault_articles WHERE user_id = ? AND subject_id = ?`, h.UserID, id); err != nil {
			return fmt.Errorf("cortex: delete article: %w", err)
		}
	}
	return nil
}

// TopicRow is one persisted topic cluster.
type TopicRow struct {
	TopicID, ShortID, Slug, Title, WrittenTitle string
	Members                                     []string
}

// PutTopics applies rows as a diff in one transaction: present topics are
// upserted, missing ones deleted. Existing rows are never dropped and
// recreated. The plaintext member_count is a conscious leak of cluster size.
func (h *Handle) PutTopics(ctx context.Context, rows []TopicRow) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: put topics: %w", err)
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, r := range rows {
		keep[r.TopicID] = true
		slug, err := h.SealStr(tblVaultTopics, r.TopicID, "slug", r.Slug)
		if err != nil {
			return err
		}
		mem, err := h.SealJSON(tblVaultTopics, r.TopicID, "member_ids", r.Members)
		if err != nil {
			return err
		}
		title, err := h.SealStr(tblVaultTopics, r.TopicID, "title", r.Title)
		if err != nil {
			return err
		}
		wt, err := h.SealStr(tblVaultTopics, r.TopicID, "written_title", r.WrittenTitle)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vault_topics (user_id, topic_id, short_id, slug_ct, member_ids_ct, title_ct, written_title_ct, member_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (user_id, topic_id) DO UPDATE SET short_id = excluded.short_id, slug_ct = excluded.slug_ct,
			   member_ids_ct = excluded.member_ids_ct, title_ct = excluded.title_ct,
			   written_title_ct = excluded.written_title_ct, member_count = excluded.member_count`,
			h.UserID, r.TopicID, r.ShortID, slug, mem, title, wt, len(r.Members)); err != nil {
			return fmt.Errorf("cortex: put topic: %w", err)
		}
	}
	rs, err := tx.QueryContext(ctx, `SELECT topic_id FROM vault_topics WHERE user_id = ?`, h.UserID)
	if err != nil {
		return err
	}
	var drop []string
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			rs.Close()
			return err
		}
		if !keep[id] {
			drop = append(drop, id)
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	for _, id := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM vault_topics WHERE user_id = ? AND topic_id = ?`, h.UserID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetTopics returns every persisted topic.
func (h *Handle) GetTopics(ctx context.Context) ([]TopicRow, error) {
	if err := h.RequireKey(); err != nil {
		return nil, err
	}
	rs, err := h.DB.QueryContext(ctx,
		`SELECT topic_id, short_id, slug_ct, member_ids_ct, title_ct, written_title_ct
		 FROM vault_topics WHERE user_id = ? ORDER BY created_at, topic_id`, h.UserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: get topics: %w", err)
	}
	defer rs.Close()
	var out []TopicRow
	for rs.Next() {
		var r TopicRow
		var slug, mem, title, wt []byte
		if err := rs.Scan(&r.TopicID, &r.ShortID, &slug, &mem, &title, &wt); err != nil {
			return nil, err
		}
		if r.Slug, err = h.OpenOptStr(tblVaultTopics, r.TopicID, "slug", slug); err != nil {
			return nil, err
		}
		if err = h.OpenJSON(tblVaultTopics, r.TopicID, "member_ids", mem, &r.Members); err != nil {
			return nil, err
		}
		if r.Title, err = h.OpenOptStr(tblVaultTopics, r.TopicID, "title", title); err != nil {
			return nil, err
		}
		if r.WrittenTitle, err = h.OpenOptStr(tblVaultTopics, r.TopicID, "written_title", wt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// SetTopicWrittenTitle stores the model-written title of a topic.
func (h *Handle) SetTopicWrittenTitle(ctx context.Context, topicID, title string) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	ct, err := h.SealStr(tblVaultTopics, topicID, "written_title", title)
	if err != nil {
		return err
	}
	_, err = h.DB.ExecContext(ctx,
		`UPDATE vault_topics SET written_title_ct = ? WHERE user_id = ? AND topic_id = ?`, ct, h.UserID, topicID)
	if err != nil {
		return fmt.Errorf("cortex: set topic title: %w", err)
	}
	return nil
}

// GetVaultSettings opens the sealed settings into out; model.ErrNotFound when
// none are saved.
func (h *Handle) GetVaultSettings(ctx context.Context, out any) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	var ct []byte
	err := h.DB.QueryRowContext(ctx, `SELECT settings_ct FROM vault_settings WHERE user_id = ?`, h.UserID).Scan(&ct)
	if err == sql.ErrNoRows {
		return model.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("cortex: get vault settings: %w", err)
	}
	return h.OpenJSON(tblVaultSettings, h.UserID, "settings", ct, out)
}

// PutVaultSettings seals and saves the settings.
func (h *Handle) PutVaultSettings(ctx context.Context, v any) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	ct, err := h.SealJSON(tblVaultSettings, h.UserID, "settings", v)
	if err != nil {
		return err
	}
	_, err = h.DB.ExecContext(ctx,
		`INSERT INTO vault_settings (user_id, settings_ct, updated_at) VALUES (?, ?, now())
		 ON CONFLICT (user_id) DO UPDATE SET settings_ct = excluded.settings_ct, updated_at = excluded.updated_at`,
		h.UserID, ct)
	if err != nil {
		return fmt.Errorf("cortex: put vault settings: %w", err)
	}
	return nil
}

// WriteRecord is one logged article-write attempt.
type WriteRecord struct {
	At     time.Time
	Tokens int
	OK     bool
}

// RecordWrite logs an attempt (no content) and prunes to the last 50.
func (h *Handle) RecordWrite(ctx context.Context, tokens int, ok bool) error {
	if _, err := h.DB.ExecContext(ctx,
		`INSERT INTO vault_writes (user_id, tokens, ok) VALUES (?, ?, ?)`, h.UserID, tokens, ok); err != nil {
		return fmt.Errorf("cortex: record write: %w", err)
	}
	_, err := h.DB.ExecContext(ctx,
		`DELETE FROM vault_writes WHERE user_id = ? AND id NOT IN
		   (SELECT id FROM vault_writes WHERE user_id = ? ORDER BY id DESC LIMIT 50)`, h.UserID, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: prune writes: %w", err)
	}
	return nil
}

// RecentWrites returns up to n attempts, newest first.
func (h *Handle) RecentWrites(ctx context.Context, n int) ([]WriteRecord, error) {
	rs, err := h.DB.QueryContext(ctx,
		`SELECT at, tokens, ok FROM vault_writes WHERE user_id = ? ORDER BY id DESC LIMIT ?`, h.UserID, n)
	if err != nil {
		return nil, fmt.Errorf("cortex: recent writes: %w", err)
	}
	defer rs.Close()
	var out []WriteRecord
	for rs.Next() {
		var w WriteRecord
		if err := rs.Scan(&w.At, &w.Tokens, &w.OK); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rs.Err()
}
