package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func TestPutSettingsFirstSaveAndMasking(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	if _, err := e.st.GetSettings(ctx, u); !errors.Is(err, ErrNoSettings) {
		t.Fatalf("got %v", err)
	}
	re, err := e.st.PutSettings(ctx, u, okSettings())
	if err != nil || re {
		t.Fatalf("first save: %v %v", re, err)
	}
	g, err := e.st.GetSettings(ctx, u)
	if err != nil || g.APIKey != "sk-secret-3f9a" || g.Masked().APIKey != "••••3f9a" {
		t.Fatalf("%+v %v", g, err)
	}
	var raw string
	e.st.DB.QueryRow(`SELECT api_key_sealed FROM user_settings WHERE user_id=$1`, u).Scan(&raw)
	if strings.Contains(raw, "secret") {
		t.Fatal("key stored in plaintext")
	}
	act, err := e.st.GetActiveEmbed(ctx, u)
	if err != nil || act.EmbedModel != "e" || act.EmbedDims != 512 || act.EmbedAPIKey != "sk-secret-3f9a" {
		t.Fatalf("%+v %v", act, err)
	}
	// private URL refused, nothing saved
	bad := okSettings()
	bad.BaseURL = "https://10.0.0.5/v1"
	if _, err := e.st.PutSettings(ctx, u, bad); err == nil {
		t.Fatal("private URL saved")
	}
}

func insertEmbedding(t *testing.T, e *env, user string) {
	t.Helper()
	// a halfvec(8) value; the column is untyped halfvec so any length works
	if _, err := e.st.DB.Exec(`INSERT INTO embeddings (id, ref_id, ref_type, user_id, embedding, dims, generation)
		VALUES ('emb1','r1','memory',$1,'[1,2,3,4,5,6,7,8]'::halfvec,8,1)`, user); err != nil {
		t.Fatal(err)
	}
}

func TestPutSettingsReembedDecision(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	e.st.PutSettings(ctx, u, okSettings())

	// no data: model/dims change promotes at once
	s := okSettings()
	s.EmbedModel, s.EmbedDims = "e2", 1024
	if re, err := e.st.PutSettings(ctx, u, s); err != nil || re {
		t.Fatalf("no-data change: %v %v", re, err)
	}
	if a, _ := e.st.GetActiveEmbed(ctx, u); a.EmbedModel != "e2" || a.EmbedDims != 1024 {
		t.Fatalf("not promoted: %+v", a)
	}

	insertEmbedding(t, e, u)
	// data present, embed key only: promoted at once with the new key
	s.EmbedAPIKey = "new-embed-key"
	if re, err := e.st.PutSettings(ctx, u, s); err != nil || re {
		t.Fatalf("key-only change: %v %v", re, err)
	}
	if a, _ := e.st.GetActiveEmbed(ctx, u); a.EmbedAPIKey != "new-embed-key" {
		t.Fatalf("key not swapped: %+v", a)
	}
	// chat-only change: nothing to re-embed
	s.ChatModel = "other-chat"
	if re, err := e.st.PutSettings(ctx, u, s); err != nil || re {
		t.Fatalf("chat change: %v %v", re, err)
	}
	// data present, model change: needs re-embed, active untouched
	s.EmbedModel = "e3"
	re, err := e.st.PutSettings(ctx, u, s)
	if err != nil || !re {
		t.Fatalf("model change: %v %v", re, err)
	}
	if a, _ := e.st.GetActiveEmbed(ctx, u); a.EmbedModel != "e2" {
		t.Fatalf("active changed early: %+v", a)
	}
	g, _ := e.st.GetSettings(ctx, u)
	if g.EmbedModel != "e3" {
		t.Fatal("desired not saved")
	}
	tx, _ := e.st.DB.Begin()
	if err := e.st.PromoteActiveEmbedTx(ctx, tx, u); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if a, _ := e.st.GetActiveEmbed(ctx, u); a.EmbedModel != "e3" {
		t.Fatalf("promote failed: %+v", a)
	}
}

func TestAgentTokenResolves(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	tok, conn, err := e.st.CreateAgentToken(ctx, u, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	uid, cid, _, err := e.st.ResolveBearer(ctx, tok)
	if err != nil || uid != u || cid != conn.ID {
		t.Fatalf("%s %s %v", uid, cid, err)
	}
	cs, _ := e.st.ListConnections(ctx, u)
	if len(cs) != 1 || cs[0].LastUsed == nil {
		t.Fatalf("last_used not set: %+v", cs)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, "nope"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

func TestRevokedTokenFails(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	tok, conn, _ := e.st.CreateAgentToken(ctx, u, "x", nil)
	if err := e.st.RevokeConnection(ctx, u, conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, tok); err == nil {
		t.Fatal("revoked token resolved")
	}
}

func TestTokensStoredHashed(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "a")
	tok, _, _ := e.st.CreateAgentToken(context.Background(), u, "x", nil)
	var id, conn, hash string
	if err := e.st.DB.QueryRow(`SELECT id, connection_id, token_hash FROM agent_tokens`).Scan(&id, &conn, &hash); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{id, conn, hash} {
		if strings.Contains(v, tok) || v == tok {
			t.Fatal("plaintext stored")
		}
	}
	if hash != HashToken(tok) {
		t.Fatal("not a sha256 of the token")
	}
}

func TestRevokeForeignConnectionNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.user(t, "a"), e.user(t, "b")
	_, conn, _ := e.st.CreateAgentToken(ctx, b, "x", nil)
	e1 := e.st.RevokeConnection(ctx, a, conn.ID)
	e2 := e.st.RevokeConnection(ctx, a, "c_absent")
	if !errors.Is(e1, ErrNotFound) || !errors.Is(e2, ErrNotFound) || e1.Error() != e2.Error() {
		t.Fatalf("%v / %v", e1, e2)
	}
	cs, _ := e.st.ListConnections(ctx, b)
	if cs[0].RevokedAt != nil {
		t.Fatal("foreign revoke took effect")
	}
}

func addOAuthToken(t *testing.T, e *env, user, conn, tok, kind string, ttl time.Duration) {
	t.Helper()
	if _, err := e.st.DB.Exec(`INSERT INTO oauth_tokens (token_hash, kind, grant_id, connection_id, user_id, expires_at)
		VALUES ($1,$2,'g1',$3,$4,$5)`, HashToken(tok), kind, conn, user, e.now.Add(ttl)); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthAccessTokenExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	c, _ := e.st.CreateConnection(ctx, u, "oauth", "Claude", "client1")
	addOAuthToken(t, e, u, c.ID, "acc", "access", time.Hour)
	addOAuthToken(t, e, u, c.ID, "ref", "refresh", time.Hour)
	if uid, cid, _, err := e.st.ResolveBearer(ctx, "acc"); err != nil || uid != u || cid != c.ID {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, "ref"); err == nil {
		t.Fatal("refresh token accepted as bearer")
	}
	e.now = e.now.Add(2 * time.Hour)
	if _, _, _, err := e.st.ResolveBearer(ctx, "acc"); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestDeletedAccountTokensRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	agent, _, _ := e.st.CreateAgentToken(ctx, u, "x", nil)
	c, _ := e.st.CreateConnection(ctx, u, "oauth", "Claude", "client1")
	addOAuthToken(t, e, u, c.ID, "acc", "access", time.Hour)
	e.st.DB.Exec(`INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('s1',$1,'c',$2)`, u, e.now.Add(time.Hour))
	e.st.DB.Exec(`INSERT INTO oauth_codes (code_hash, user_id, connection_id, expires_at) VALUES ('code1',$1,$2,$3)`, u, c.ID, e.now.Add(time.Minute))

	if err := e.st.DeleteAccount(ctx, u); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{agent, "acc"} {
		if _, _, _, err := e.st.ResolveBearer(ctx, tok); err == nil {
			t.Fatalf("token %q still resolves", tok)
		}
	}
	cs, _ := e.st.ListConnections(ctx, u)
	for _, c := range cs {
		if c.RevokedAt == nil {
			t.Fatalf("connection %s not revoked", c.ID)
		}
	}
	for _, tbl := range []string{"oauth_tokens", "sessions", "oauth_codes"} {
		var n int
		e.st.DB.QueryRow(`SELECT count(*) FROM `+tbl+` WHERE user_id=$1`, u).Scan(&n)
		if n != 0 {
			t.Fatalf("%s has %d rows", tbl, n)
		}
	}
	if err := e.st.DeleteAccount(ctx, "u_absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	if err := e.st.RestoreAccount(ctx, u); err != nil {
		t.Fatal(err)
	}
	cs, _ = e.st.ListConnections(ctx, u)
	for _, c := range cs {
		if c.RevokedAt == nil {
			t.Fatal("restored account has live connection")
		}
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, agent); err == nil {
		t.Fatal("old token works after restore")
	}
}

func TestOAuthConnectionPartialUnique(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.user(t, "a"), e.user(t, "b")
	c1, err := e.st.CreateConnection(ctx, a, "oauth", "Claude", "cid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateConnection(ctx, a, "oauth", "Claude", "cid"); err == nil {
		t.Fatal("second live oauth connection allowed")
	}
	if err := e.st.RevokeConnection(ctx, a, c1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateConnection(ctx, a, "oauth", "Claude", "cid"); err != nil {
		t.Fatalf("after revoke: %v", err)
	}
	if _, err := e.st.CreateConnection(ctx, b, "oauth", "Claude", "cid"); err != nil {
		t.Fatalf("other user: %v", err)
	}
}

func TestListConnectionsWrittenCount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	c, _ := e.st.CreateConnection(ctx, u, "oauth", "Claude", "cid")
	e.st.DB.Exec(`INSERT INTO memories (id, user_id, content, written_by_connection_id) VALUES ('m1',$1,'x',$2),('m2',$1,'y',$2)`, u, c.ID)
	cs, _ := e.st.ListConnections(ctx, u)
	if cs[0].Written != 2 {
		t.Fatalf("written %d", cs[0].Written)
	}
}

func bootEnv() map[string]string {
	return map[string]string{"OPENAI_API_KEY": "sk-op", "CORTEX_AUTH_TOKEN": "tunnel-token", "LLM_MODEL": "gpt-x"}
}

func TestMigrateBootstrapIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := MigrateBootstrap(ctx, e.st, bootEnv()); err != nil {
			t.Fatal(err)
		}
	}
	var n, m int
	e.st.DB.QueryRow(`SELECT count(*) FROM user_settings`).Scan(&n)
	e.st.DB.QueryRow(`SELECT count(*) FROM agent_tokens`).Scan(&m)
	if n != 1 || m != 1 {
		t.Fatalf("settings %d tokens %d", n, m)
	}
	uid, _, _, err := e.st.ResolveBearer(ctx, "tunnel-token")
	if err != nil || uid != cortex.BootstrapUserID {
		t.Fatalf("%s %v", uid, err)
	}
	s, _ := e.st.GetSettings(ctx, cortex.BootstrapUserID)
	if s.APIKey != "sk-op" || s.ChatModel != "gpt-x" || s.BaseURL != DefaultBaseURL || s.EmbedDims != 512 || s.EmbedModel != DefaultEmbedModel {
		t.Fatalf("%+v", s)
	}
	if a, err := e.st.GetActiveEmbed(ctx, cortex.BootstrapUserID); err != nil || a.EmbedDims != 512 {
		t.Fatal(err)
	}
	cs, _ := e.st.ListConnections(ctx, cortex.BootstrapUserID)
	if cs[0].Name != "claude-code (tunnel)" {
		t.Fatalf("%+v", cs)
	}
}

func TestMigrateBootstrapNeverResurrects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	MigrateBootstrap(ctx, e.st, bootEnv())
	cs, _ := e.st.ListConnections(ctx, cortex.BootstrapUserID)
	e.st.RevokeConnection(ctx, cortex.BootstrapUserID, cs[0].ID)
	if err := MigrateBootstrap(ctx, e.st, bootEnv()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, "tunnel-token"); err == nil {
		t.Fatal("revoked tunnel token works again")
	}
	cs, _ = e.st.ListConnections(ctx, cortex.BootstrapUserID)
	if len(cs) != 1 {
		t.Fatalf("%d connections", len(cs))
	}
}

func TestMigrateBootstrapNeedsKey(t *testing.T) {
	e := newEnv(t)
	err := MigrateBootstrap(context.Background(), e.st, map[string]string{"CORTEX_AUTH_TOKEN": "t"})
	if err == nil {
		t.Fatal("want error")
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM agent_tokens`).Scan(&n)
	if n != 0 {
		t.Fatal("token created despite failure")
	}
}

func TestBootstrapOwnerEntityIsOwner(t *testing.T) { TestMigrateBootstrapSetsOwnerEntity(t) }

func TestMigrateBootstrapSetsOwnerEntity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := MigrateBootstrap(ctx, e.st, bootEnv()); err != nil {
		t.Fatal(err)
	}
	var owner sql.NullString
	e.st.DB.QueryRow(`SELECT owner_entity_id FROM users WHERE id='u_bootstrap'`).Scan(&owner)
	if !owner.Valid {
		t.Fatal("owner_entity_id not set")
	}
	var src string
	var isOwner bool
	e.st.DB.QueryRow(`SELECT source, is_owner FROM entities WHERE id=$1`, owner.String).Scan(&src, &isOwner)
	if src != "owner" || !isOwner {
		t.Fatalf("source %q is_owner %v", src, isOwner)
	}
	// existing owner entity is adopted
	e2 := newEnv(t)
	e2.st.DB.Exec(`INSERT INTO entities (id, user_id, type, name, source) VALUES ('legacy','u_bootstrap','person','Sau','x')`)
	e2.st.DB.Exec(`UPDATE entities SET is_owner = TRUE WHERE id = 'legacy'`)
	if err := MigrateBootstrap(ctx, e2.st, bootEnv()); err != nil {
		t.Fatal(err)
	}
	e2.st.DB.QueryRow(`SELECT owner_entity_id FROM users WHERE id='u_bootstrap'`).Scan(&owner)
	if owner.String != "legacy" {
		t.Fatalf("got %q", owner.String)
	}
}

func TestResolveUserNoSettings(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "a")
	// operator key in env must never be used: no settings means ErrNoProvider.
	t.Setenv("OPENAI_API_KEY", "sk-operator")
	_, err := e.st.ResolveUser(context.Background(), e.root, u, "c1", nil)
	if !errors.Is(err, ErrNoProvider) || !strings.Contains(err.Error(), "/settings") {
		t.Fatalf("got %v", err)
	}
}

func TestUserDeleteCascadesAllTables(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	e.st.PutSettings(ctx, u, okSettings())
	_, conn, _ := e.st.CreateAgentToken(ctx, u, "x", nil)
	oc, _ := e.st.CreateConnection(ctx, u, "oauth", "C", "cid")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO jobs (id, user_id, kind) VALUES ('j1',$1,'index')`, u)
	exec(`INSERT INTO compactions (id, user_id, status) VALUES ('k1',$1,'ready')`, u)
	exec(`INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('s1',$1,'c',now())`, u)
	exec(`INSERT INTO oauth_codes (code_hash, user_id, connection_id, expires_at) VALUES ('cd',$1,$2,now())`, u, oc.ID)
	exec(`INSERT INTO oauth_tokens (token_hash, kind, connection_id, user_id, expires_at) VALUES ('tk','access',$1,$2,now())`, oc.ID, u)
	exec(`INSERT INTO authorize_requests (id, user_id, client_id, redirect_uri, code_challenge, expires_at) VALUES ('ar',$1,'cid','https://x','ch',now())`, u)
	exec(`INSERT INTO memories (id, user_id, content, written_by_connection_id) VALUES ('m1',$1,'x',$2)`, u, conn.ID)
	exec(`INSERT INTO entities (id, user_id, type, name) VALUES ('e1',$1,'person','Al')`, u)
	exec(`INSERT INTO delete_batches (id, user_id, kind) VALUES ('b1',$1,'delete')`, u)
	exec(`INSERT INTO delete_batch_changes (batch_id, user_id, table_name, op, row_json) VALUES ('b1',$1,'memories','delete','{}')`, u)

	// every table with a user_id column has a cascading FK to users
	rows, err := e.st.DB.Query(`SELECT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name AND t.table_type='BASE TABLE'
		WHERE c.table_schema = current_schema() AND c.column_name = 'user_id'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 15 {
		t.Fatalf("only %d tables with user_id: %v", len(tables), tables)
	}
	for _, tbl := range tables {
		var del string
		err := e.st.DB.QueryRow(`SELECT con.confdeltype::text FROM pg_constraint con
			JOIN pg_class cl ON cl.oid = con.conrelid
			JOIN pg_namespace ns ON ns.oid = cl.relnamespace AND ns.nspname = current_schema()
			JOIN pg_attribute a ON a.attrelid = cl.oid AND a.attnum = ANY(con.conkey)
			WHERE con.contype='f' AND cl.relname=$1 AND a.attname='user_id'
			AND con.confrelid = 'users'::regclass`, tbl).Scan(&del)
		if err != nil || del != "c" {
			t.Errorf("table %s: FK to users cascade missing (%q, %v)", tbl, del, err)
		}
	}

	if _, err := e.st.DB.Exec(`DELETE FROM users WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		var n int
		e.st.DB.QueryRow(`SELECT count(*) FROM `+tbl+` WHERE user_id=$1`, u).Scan(&n)
		if n != 0 {
			t.Errorf("%s has %d rows after user delete", tbl, n)
		}
	}
	for _, tbl := range []string{"agent_tokens"} {
		var n int
		e.st.DB.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n)
		if n != 0 {
			t.Errorf("%s has %d rows after user delete", tbl, n)
		}
	}
}

func TestResolveUserEncryptedWithoutKeyIsLocked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "enc")
	if _, err := e.st.PutSettings(ctx, u, okSettings()); err != nil {
		t.Fatal(err)
	}
	if cx, err := e.st.ResolveUser(ctx, e.root, u, "c1", nil); err != nil || cx.Encrypted() {
		t.Fatalf("state none: %v", err)
	}
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE users SET encryption_state='encrypted' WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ResolveUser(ctx, e.root, u, "c1", nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("no key: %v", err)
	}
	uk, _ := keys.NewUK()
	defer uk.Zero()
	cx, err := e.st.ResolveUser(ctx, e.root, u, "c1", uk)
	if err != nil || !cx.Encrypted() {
		t.Fatalf("with key: %v", err)
	}
	cx.Release()
	if uk.Bytes() == nil {
		t.Fatal("caller key zeroed")
	}
}

func TestResolveBearerEncryptedNeedsReconnect(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "encb")
	tok, _, err := e.st.CreateAgentToken(ctx, u, "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE users SET encryption_state='encrypted' WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, tok); !errors.Is(err, model.ErrNeedsReconnect) {
		t.Fatalf("got %v", err)
	}
}
