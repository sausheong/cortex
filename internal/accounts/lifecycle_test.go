package accounts

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/webauthntest"
)

func TestRemovePasskeyDeletesWrapAndRow(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, prf, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	// Add a second passkey so removal needs no recovery key.
	var credID string
	p.st.DB.QueryRow(`SELECT id FROM webauthn_credentials WHERE user_id=$1`, p.user).Scan(&credID)
	other := newSecondDevice(t, p, res)
	if err := p.st.RemovePasskey(ctx, p.user, credID, res.UK, ""); err != nil {
		t.Fatal(err)
	}
	if n := p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='passkey' AND ref_id=$2`, p.user, credID); n != 0 {
		t.Fatal("wrap row left")
	}
	if n := p.count(t, `SELECT count(*) FROM webauthn_credentials WHERE id=$1`, credID); n != 0 {
		t.Fatal("credential row left")
	}
	// The removed passkey's PRF cannot unwrap anything: no wrap carries its ref.
	if _, err := p.st.GetWrap(ctx, p.user, "passkey", credID); err == nil {
		t.Fatal("wrap still readable")
	}
	_ = prf
	_ = other
	if _, err := p.unlock(t, p.dev, nil); err == nil {
		t.Fatal("removed passkey still unlocks")
	}
}

func newSecondDevice(t *testing.T, p *pkEnv, res *EnrollResult) *webauthntest.Authenticator {
	t.Helper()
	ctx := context.Background()
	second := webauthntest.New(t, pkRPID, pkOrigin)
	c, err := p.st.BeginAddPasskey(ctx, p.user)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.st.AddPasskey(ctx, p.user, res.UK, c.ID, second.Create(t, c.Options), second.PRFFromCreate(t, c.Options), "Phone"); err != nil {
		t.Fatal(err)
	}
	return second
}

func TestRegenerateRecoveryDeletesOldWrapRow(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	var before []byte
	p.st.DB.QueryRow(`SELECT wrapped_uk FROM user_key_wraps WHERE user_id=$1 AND kind='recovery'`, p.user).Scan(&before)
	if _, err := p.st.RegenerateRecovery(ctx, p.user, res.UK); err != nil {
		t.Fatal(err)
	}
	var after []byte
	p.st.DB.QueryRow(`SELECT wrapped_uk FROM user_key_wraps WHERE user_id=$1 AND kind='recovery'`, p.user).Scan(&after)
	if n := p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='recovery'`, p.user); n != 1 {
		t.Fatalf("%d recovery rows", n)
	}
	if bytes.Equal(before, after) {
		t.Fatal("wrapped bytes unchanged")
	}
}

func TestOldRecoveryKeyFailsAfterRegenerate(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	if _, err := p.st.RegenerateRecovery(ctx, p.user, res.UK); err != nil {
		t.Fatal(err)
	}
	if _, err := p.st.UnlockWithRecovery(ctx, p.user, res.Recovery); err == nil {
		t.Fatal("old recovery key works")
	}
}

// seedWraps gives an encrypted user passkey, recovery, session, agent and oauth wraps.
func seedWraps(t *testing.T, e *env, u string, uk *keys.UK) (agentTok string) {
	t.Helper()
	ctx := context.Background()
	for _, k := range [][2]string{{"passkey", "pk1"}, {"recovery", "recovery"}, {"session", "s1"}, {"oauth_code", "c1"}, {"oauth_access", "a1"}, {"oauth_refresh", "r1"}} {
		w, err := keys.Wrap(keys.WrapKeyRecovery(bytes.Repeat([]byte{1}, 32)), u, k[0], k[1], uk)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.PutWrap(ctx, Wrap{UserID: u, Kind: k[0], RefID: k[1], Wrapped: w}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.DB.Exec(`INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('s1',$1,'c',$2)`, u, e.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tok, _, err := e.st.CreateAgentToken(ctx, u, "a", uk)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func wrapKinds(t *testing.T, e *env, u string) map[string]int {
	t.Helper()
	rows, err := e.st.DB.Query(`SELECT kind, count(*) FROM user_key_wraps WHERE user_id=$1 GROUP BY kind`, u)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		rows.Scan(&k, &n)
		m[k] = n
	}
	return m
}

func TestSoftDeleteRemovesSessionAgentOAuthWraps(t *testing.T) {
	e := newEnv(t)
	u, uk := e.encrypted(t, "d1")
	seedWraps(t, e, u, uk)
	var dropped string
	old := WorksetDrop
	WorksetDrop = func(id string) { dropped = id }
	defer func() { WorksetDrop = old }()
	if err := e.st.DeleteAccount(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	m := wrapKinds(t, e, u)
	if len(m) != 2 || m["passkey"] != 1 || m["recovery"] != 1 {
		t.Fatalf("wraps after delete: %v", m)
	}
	if dropped != u {
		t.Fatal("workset drop hook not called")
	}
}

func TestSoftDeletedAccountTokenAndSessionCannotUnwrap(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "d2")
	tok := seedWraps(t, e, u, uk)
	if err := e.st.DeleteAccount(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, _, got, err := e.st.ResolveBearer(ctx, tok); err == nil || got != nil {
		t.Fatal("token resolved after delete")
	}
	if _, err := e.st.GetWrap(ctx, u, "session", "s1"); err == nil {
		t.Fatal("session wrap readable after delete")
	}
}

func TestRestoreAccountDoesNotResurrectWraps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "d3")
	tok := seedWraps(t, e, u, uk)
	if err := e.st.DeleteAccount(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RestoreAccount(ctx, u); err != nil {
		t.Fatal(err)
	}
	m := wrapKinds(t, e, u)
	if len(m) != 2 || m["passkey"] != 1 || m["recovery"] != 1 {
		t.Fatalf("wraps after restore: %v", m)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, tok); err == nil {
		t.Fatal("old token works after restore")
	}
}
