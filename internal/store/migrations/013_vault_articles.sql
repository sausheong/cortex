-- Vault articles: sealed per-subject articles, topics, settings and a usage log.
-- Content columns are ciphertext. Slugs embed entity names, so only a sealed
-- slug and a plaintext short id (the tail of a random id) are stored.

CREATE TABLE IF NOT EXISTS vault_articles (
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	subject_id  TEXT NOT NULL,
	kind        TEXT NOT NULL,
	short_id    TEXT NOT NULL,
	slug_ct     BYTEA,
	fingerprint BYTEA NOT NULL,
	written_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	stale       BOOLEAN NOT NULL DEFAULT false,
	tokens      INT NOT NULL DEFAULT 0,
	article_ct  BYTEA,
	PRIMARY KEY (user_id, subject_id),
	UNIQUE (user_id, short_id)
);

-- member_count is a documented, conscious plaintext leak of cluster size.
CREATE TABLE IF NOT EXISTS vault_topics (
	user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	topic_id         TEXT NOT NULL,
	short_id         TEXT NOT NULL,
	slug_ct          BYTEA,
	member_ids_ct    BYTEA,
	title_ct         BYTEA,
	written_title_ct BYTEA,
	member_count     INT NOT NULL DEFAULT 0,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (user_id, topic_id),
	UNIQUE (user_id, short_id)
);

CREATE TABLE IF NOT EXISTS vault_settings (
	user_id     TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	settings_ct BYTEA NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Attempt and usage log without content; pruned to the last 50 per user.
CREATE TABLE IF NOT EXISTS vault_writes (
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	id      BIGSERIAL,
	at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	tokens  INT NOT NULL DEFAULT 0,
	ok      BOOLEAN NOT NULL DEFAULT true,
	PRIMARY KEY (user_id, id)
);
