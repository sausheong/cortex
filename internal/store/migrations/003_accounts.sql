-- Accounts: provider settings, connections, tokens, sessions, OAuth server
-- state, jobs and compactions. Complete DDL for Tasks 6-13; later tasks only
-- reference these tables.

CREATE TABLE user_settings (
	user_id             TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	provider            TEXT NOT NULL DEFAULT 'openai-compatible',
	base_url            TEXT NOT NULL DEFAULT '',
	api_key_sealed      TEXT NOT NULL DEFAULT '',
	chat_model          TEXT NOT NULL DEFAULT '',
	embed_model         TEXT NOT NULL DEFAULT '',
	embed_dims          INT  NOT NULL DEFAULT 512,
	embed_base_url      TEXT NOT NULL DEFAULT '',
	embed_key_sealed    TEXT NOT NULL DEFAULT '',
	active_embed_sealed TEXT NOT NULL DEFAULT '',
	updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE connections (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL CHECK (kind IN ('oauth', 'agent')),
	name       TEXT NOT NULL DEFAULT '',
	client_id  TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	last_used  TIMESTAMPTZ,
	revoked_at TIMESTAMPTZ
);
CREATE INDEX idx_connections_user ON connections(user_id);
CREATE UNIQUE INDEX idx_connections_oauth_live ON connections(user_id, client_id)
	WHERE kind = 'oauth' AND revoked_at IS NULL;

CREATE TABLE agent_tokens (
	id            TEXT PRIMARY KEY,
	connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
	token_hash    TEXT NOT NULL UNIQUE
);
CREATE INDEX idx_agent_tokens_conn ON agent_tokens(connection_id);

CREATE TABLE oauth_clients (
	id            TEXT PRIMARY KEY,
	kind          TEXT NOT NULL CHECK (kind IN ('dcr', 'cimd')),
	name          TEXT NOT NULL DEFAULT '',
	redirect_uris JSONB NOT NULL DEFAULT '[]',
	cimd_host     TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	authorized_at TIMESTAMPTZ,
	expires_at    TIMESTAMPTZ
);

CREATE TABLE authorize_requests (
	id             TEXT PRIMARY KEY,
	user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id      TEXT NOT NULL,
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL,
	state          TEXT NOT NULL DEFAULT '',
	scope          TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_authorize_requests_user ON authorize_requests(user_id);

CREATE TABLE oauth_codes (
	code_hash      TEXT PRIMARY KEY,
	request_fields JSONB NOT NULL DEFAULT '{}',
	user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	connection_id  TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
	expires_at     TIMESTAMPTZ NOT NULL,
	used_at        TIMESTAMPTZ
);
CREATE INDEX idx_oauth_codes_user ON oauth_codes(user_id);

CREATE TABLE oauth_tokens (
	token_hash    TEXT PRIMARY KEY,
	kind          TEXT NOT NULL CHECK (kind IN ('access', 'refresh')),
	grant_id      TEXT NOT NULL DEFAULT '',
	connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
	user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at    TIMESTAMPTZ NOT NULL,
	last_used_at  TIMESTAMPTZ,
	revoked_at    TIMESTAMPTZ
);
CREATE INDEX idx_oauth_tokens_user ON oauth_tokens(user_id);
CREATE INDEX idx_oauth_tokens_conn ON oauth_tokens(connection_id);

CREATE TABLE sessions (
	id_hash    TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE login_attempts (
	id_hash    TEXT PRIMARY KEY,
	state      TEXT NOT NULL,
	nonce      TEXT NOT NULL,
	return_to  TEXT NOT NULL DEFAULT '/',
	expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE compactions (
	id            TEXT PRIMARY KEY,
	user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	status        TEXT NOT NULL CHECK (status IN ('proposing', 'ready', 'applied', 'undone', 'failed')),
	proposal_json JSONB,
	batch_id      TEXT,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_compactions_user ON compactions(user_id);

CREATE TABLE jobs (
	id             TEXT PRIMARY KEY,
	user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind           TEXT NOT NULL,
	status         TEXT NOT NULL DEFAULT 'queued',
	progress_done  INT NOT NULL DEFAULT 0,
	progress_total INT NOT NULL DEFAULT 0,
	error          TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_jobs_user ON jobs(user_id);
CREATE INDEX idx_jobs_status ON jobs(status);
