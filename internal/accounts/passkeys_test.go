package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/model"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/webauthntest"
)

const (
	pkRPID   = "cortex.test"
	pkOrigin = "https://cortex.test"
)

type pkEnv struct {
	*env
	user string
	dev  *webauthntest.Authenticator
}

func newPK(t *testing.T) *pkEnv {
	t.Helper()
	e := newEnv(t)
	if err := e.st.ConfigurePasskeys(PasskeyConfig{RPID: pkRPID, Origins: []string{pkOrigin}, EnrollEnabled: true}); err != nil {
		t.Fatal(err)
	}
	u, err := e.root.CreateUser(context.Background(), "g-pk", "pk@x.test", "Pk", true)
	if err != nil {
		t.Fatal(err)
	}
	return &pkEnv{env: e, user: u.ID, dev: webauthntest.New(t, pkRPID, pkOrigin)}
}

// enroll runs Begin and Finish and returns the result and the PRF output.
func (p *pkEnv) enroll(t *testing.T) (*EnrollResult, []byte) {
	t.Helper()
	ctx := context.Background()
	c, err := p.st.BeginEnroll(ctx, p.user)
	if err != nil {
		t.Fatal(err)
	}
	prf := p.dev.PRFFromCreate(t, c.Options)
	res, err := p.st.FinishEnroll(ctx, p.user, c.ID, p.dev.Create(t, c.Options), prf, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	return res, prf
}

func groupsFor(res *EnrollResult) []string {
	g := strings.Split(res.Recovery, "-")
	return []string{g[res.Positions[0]-1], g[res.Positions[1]-1]}
}

// confirm runs BeginConfirm and ConfirmEnroll with the given PRF and groups.
func (p *pkEnv) confirm(t *testing.T, res *EnrollResult, prfOverride []byte, groups []string) (string, *keys.UK, error) {
	t.Helper()
	ctx := context.Background()
	c, err := p.st.BeginConfirm(ctx, p.user, res.ConfirmID)
	if err != nil {
		return "", nil, err
	}
	assertion, prf := p.dev.Get(t, c.Options)
	if prfOverride != nil {
		prf = prfOverride
	}
	return p.st.ConfirmEnroll(ctx, p.user, res.ConfirmID, assertion, prf, groups)
}

func (p *pkEnv) enrollAndConfirm(t *testing.T) (*EnrollResult, []byte, string) {
	t.Helper()
	res, prf := p.enroll(t)
	state, uk, err := p.confirm(t, res, nil, groupsFor(res))
	if err != nil {
		t.Fatal(err)
	}
	if !uk.Equal(res.UK) {
		t.Fatal("confirm returned a different key")
	}
	uk.Zero()
	return res, prf, state
}

func (p *pkEnv) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := p.st.DB.QueryRow(`SELECT encryption_state FROM users WHERE id = $1`, p.user).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (p *pkEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.st.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (p *pkEnv) unlock(t *testing.T, dev *webauthntest.Authenticator, prfOverride []byte) (*keys.UK, error) {
	t.Helper()
	ctx := context.Background()
	c, err := p.st.BeginUnlock(ctx, p.user)
	if err != nil {
		return nil, err
	}
	assertion, prf := dev.Get(t, c.Options)
	if prfOverride != nil {
		prf = prfOverride
	}
	return p.st.FinishUnlock(ctx, p.user, c.ID, assertion, prf)
}

func TestEnrollCreatesWrapsAndRecovery(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, prf, state := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	if state != "encrypted" || p.state(t) != "encrypted" {
		t.Fatalf("state %q, want encrypted for an empty user", state)
	}
	for _, k := range []string{"passkey", "recovery"} {
		var pending bool
		if err := p.st.DB.QueryRow(`SELECT pending FROM user_key_wraps WHERE user_id=$1 AND kind=$2`, p.user, k).Scan(&pending); err != nil || pending {
			t.Fatalf("%s wrap: pending=%v err=%v", k, pending, err)
		}
	}
	// The recovery key and the passkey path unwrap to the same UK.
	rk, err := p.st.UnlockWithRecovery(ctx, p.user, res.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := p.unlock(t, p.dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rk.Equal(pk) || !rk.Equal(res.UK) {
		t.Fatal("unlock paths disagree")
	}
	_ = prf
}

// Confirming creates the account's person entity sealed: sign-up writes no
// data, and the entity is created under the new key.
func TestEnrollCreatesSealedOwnerEntity(t *testing.T) {
	p := newPK(t)
	if n := p.count(t, `SELECT count(*) FROM entities WHERE user_id=$1`, p.user); n != 0 {
		t.Fatalf("sign-up wrote %d entities", n)
	}
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	var id string
	if err := p.st.DB.QueryRow(`SELECT owner_entity_id FROM users WHERE id=$1`, p.user).Scan(&id); err != nil || id == "" {
		t.Fatalf("owner_entity_id = %q, %v", id, err)
	}
	var nameCT []byte
	var isOwner bool
	if err := p.st.DB.QueryRow(`SELECT name_ct, is_owner FROM entities WHERE id=$1 AND user_id=$2`, id, p.user).Scan(&nameCT, &isOwner); err != nil || !isOwner {
		t.Fatalf("owner entity: %v isOwner=%v", err, isOwner)
	}
	name, err := res.UK.Open(p.user, "entities", id, "name", nameCT)
	if err != nil || string(name) != "Pk" {
		t.Fatalf("owner name = %q, %v", name, err)
	}
}

func TestEnrollNotCompleteUntilUnlockProofAndRecoveryConfirmed(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _ := p.enroll(t)
	defer res.UK.Zero()
	if p.state(t) != "none" {
		t.Fatal("FinishEnroll must leave the state at none")
	}
	if n := p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND pending`, p.user); n != 2 {
		t.Fatalf("%d pending wraps, want 2", n)
	}
	// Pending wraps are not usable for unlock.
	if _, err := p.st.UnlockWithRecovery(ctx, p.user, res.Recovery); err == nil {
		t.Fatal("pending recovery wrap unlocked")
	}
	if _, err := p.st.BeginUnlock(ctx, p.user); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("BeginUnlock before confirm: %v", err)
	}
	// Confirm: both proofs together flip the state.
	state, uk, err := p.confirm(t, res, nil, groupsFor(res))
	if err != nil {
		t.Fatal(err)
	}
	uk.Zero()
	if state != "encrypted" || p.state(t) != "encrypted" {
		t.Fatal("not encrypted after confirm")
	}
	if n := p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND pending`, p.user); n != 0 {
		t.Fatal("wraps still pending after confirm")
	}
}

func TestWrongPRFAtConfirmFailsAndStateStaysNone(t *testing.T) {
	p := newPK(t)
	res, prf := p.enroll(t)
	defer res.UK.Zero()
	wrong := append([]byte(nil), prf...)
	wrong[0] ^= 0xff
	_, _, err := p.confirm(t, res, wrong, groupsFor(res))
	if !errors.Is(err, ErrProof) {
		t.Fatalf("want ErrProof, got %v", err)
	}
	if p.state(t) != "none" {
		t.Fatal("state changed on a wrong PRF")
	}
	if n := p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND pending`, p.user); n != 2 {
		t.Fatal("pending wraps changed")
	}
	// The right proof still works afterwards (retry within the attempt budget).
	if _, uk, err := p.confirm(t, res, nil, groupsFor(res)); err != nil {
		t.Fatal(err)
	} else {
		uk.Zero()
	}
}

func TestWrongRecoveryGroupsFailConfirm(t *testing.T) {
	p := newPK(t)
	res, _ := p.enroll(t)
	defer res.UK.Zero()
	_, _, err := p.confirm(t, res, nil, []string{"AAAA", "BBBB"})
	if !errors.Is(err, ErrProof) {
		t.Fatalf("want ErrProof, got %v", err)
	}
	// Swapped order is wrong too.
	g := groupsFor(res)
	if _, _, err := p.confirm(t, res, nil, []string{g[1], g[0]}); !errors.Is(err, ErrProof) {
		t.Fatalf("swapped groups: %v", err)
	}
	if p.state(t) != "none" {
		t.Fatal("state changed")
	}
	// Lowercase and dashes are accepted.
	_, uk, err := p.confirm(t, res, nil, []string{strings.ToLower(g[0]) + "-", " " + g[1]})
	if err != nil {
		t.Fatal(err)
	}
	uk.Zero()
}

func TestConfirmAttemptsAreLimited(t *testing.T) {
	p := newPK(t)
	res, _ := p.enroll(t)
	defer res.UK.Zero()
	for i := 0; i < maxConfirmAttempts; i++ {
		p.confirm(t, res, nil, []string{"AAAA", "BBBB"})
	}
	if _, _, err := p.confirm(t, res, nil, groupsFor(res)); err == nil {
		t.Fatal("confirm allowed after too many failures")
	}
	if p.state(t) != "none" {
		t.Fatal("state changed")
	}
}

func TestPendingEnrollmentExpires(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _ := p.enroll(t)
	defer res.UK.Zero()
	p.now = p.now.Add(PendingTTL + time.Minute)
	if _, _, err := p.confirm(t, res, nil, groupsFor(res)); err == nil {
		t.Fatal("expired enrollment confirmed")
	}
	if err := p.st.PurgeEnrollments(ctx, p.now); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM user_key_wraps WHERE user_id=$1`,
		`SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`,
		`SELECT count(*) FROM webauthn_ceremonies WHERE user_id=$1`,
	} {
		if n := p.count(t, q, p.user); n != 0 {
			t.Fatalf("%s: %d rows left", q, n)
		}
	}
	if p.state(t) != "none" {
		t.Fatal("state changed")
	}
	// The user can start over.
	res2, _ := p.enroll(t)
	res2.UK.Zero()
}

func TestEnrollBlockedWhenFlagOff(t *testing.T) {
	p := newPK(t)
	if err := p.st.ConfigurePasskeys(PasskeyConfig{RPID: pkRPID, Origins: []string{pkOrigin}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.st.BeginEnroll(context.Background(), p.user); !errors.Is(err, ErrEnrollDisabled) {
		t.Fatalf("want ErrEnrollDisabled, got %v", err)
	}
}

func TestEnrollRejectsMissingPRF(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	c, _ := p.st.BeginEnroll(ctx, p.user)
	_, err := p.st.FinishEnroll(ctx, p.user, c.ID, p.dev.Create(t, c.Options), nil, "x")
	if !errors.Is(err, ErrNoPRF) {
		t.Fatalf("want ErrNoPRF, got %v", err)
	}
	if p.state(t) != "none" || p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1`, p.user) != 0 {
		t.Fatal("rows written without PRF")
	}
}

func TestUnlockWithPasskeyAndPRF(t *testing.T) {
	p := newPK(t)
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	uk, err := p.unlock(t, p.dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer uk.Zero()
	if !uk.Equal(res.UK) {
		t.Fatal("wrong key")
	}
}

func TestUnlockWrongPRFFails(t *testing.T) {
	p := newPK(t)
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	uk, err := p.unlock(t, p.dev, make([]byte, 32))
	if !errors.Is(err, keys.ErrDecrypt) || uk != nil {
		t.Fatalf("want ErrDecrypt and no key, got %v %v", uk, err)
	}
}

func TestUnlockWithRecoveryKey(t *testing.T) {
	p := newPK(t)
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	uk, err := p.st.UnlockWithRecovery(context.Background(), p.user, strings.ToLower(res.Recovery))
	if err != nil {
		t.Fatal(err)
	}
	defer uk.Zero()
	if !uk.Equal(res.UK) {
		t.Fatal("wrong key")
	}
}

func TestUnlockRecoveryWrongKeyFails(t *testing.T) {
	p := newPK(t)
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	_, other, _ := keys.NewRecoveryKey()
	for _, bad := range []string{other, "", "not a key"} {
		if uk, err := p.st.UnlockWithRecovery(context.Background(), p.user, bad); err == nil || uk != nil {
			t.Fatalf("%q unlocked", bad)
		}
	}
}

func TestUnlockDoesNotLogOrStorePRF(t *testing.T) {
	p := newPK(t)
	res, prf, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	uk, err := p.unlock(t, p.dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	uk.Zero()
	// No stored value contains the PRF output, the recovery key or the UK.
	needles := [][]byte{prf, []byte(res.Recovery), []byte(strings.ReplaceAll(res.Recovery, "-", ""))}
	if b := res.UK.Bytes(); b != nil {
		needles = append(needles, b)
	}
	for _, tbl := range []string{"user_key_wraps", "webauthn_credentials", "webauthn_ceremonies"} {
		rows, err := p.st.DB.Query(`SELECT t::text FROM ` + tbl + ` t`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			rows.Scan(&s)
			for _, n := range needles {
				if strings.Contains(s, string(n)) || strings.Contains(s, "\\x"+hexOf(n)) {
					t.Fatalf("%s row contains secret material", tbl)
				}
			}
		}
		rows.Close()
	}
}

func hexOf(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, h[c>>4], h[c&15])
	}
	return string(out)
}

func TestWebAuthnOriginMismatchRejected(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	evil := webauthntest.New(t, pkRPID, "https://evil.test")
	c, _ := p.st.BeginEnroll(ctx, p.user)
	_, err := p.st.FinishEnroll(ctx, p.user, c.ID, evil.Create(t, c.Options), evil.PRFFromCreate(t, c.Options), "x")
	if !errors.Is(err, ErrPasskeyVerify) {
		t.Fatalf("enroll with wrong origin: %v", err)
	}
	// And for an assertion on an enrolled account.
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	bad := *p.dev
	bad.Origin = "https://evil.test"
	if _, err := p.unlock(t, &bad, nil); !errors.Is(err, ErrPasskeyVerify) {
		t.Fatalf("unlock with wrong origin: %v", err)
	}
}

func TestWebAuthnRPIDMismatchRejected(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	evil := webauthntest.New(t, "evil.test", pkOrigin)
	c, _ := p.st.BeginEnroll(ctx, p.user)
	_, err := p.st.FinishEnroll(ctx, p.user, c.ID, evil.Create(t, c.Options), evil.PRFFromCreate(t, c.Options), "x")
	if !errors.Is(err, ErrPasskeyVerify) {
		t.Fatalf("enroll with wrong rp id: %v", err)
	}
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	bad := *p.dev
	bad.RPID = "evil.test"
	if _, err := p.unlock(t, &bad, nil); !errors.Is(err, ErrPasskeyVerify) {
		t.Fatalf("unlock with wrong rp id: %v", err)
	}
}

func TestSignCountRegressionRejected(t *testing.T) {
	p := newPK(t)
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	// enrollAndConfirm used one assertion (count 1). Advance to 5 on success.
	five := uint32(5)
	p.dev.NextSignCount = &five
	uk, err := p.unlock(t, p.dev, nil)
	if err != nil {
		t.Fatal(err)
	}
	uk.Zero()
	var sc int64
	p.st.DB.QueryRow(`SELECT sign_count FROM webauthn_credentials WHERE user_id=$1`, p.user).Scan(&sc)
	if sc != 5 {
		t.Fatalf("sign_count %d, want 5 after success", sc)
	}
	// A regression (3 <= 5) is refused and does not unlock.
	three := uint32(3)
	p.dev.NextSignCount = &three
	if uk, err := p.unlock(t, p.dev, nil); !errors.Is(err, ErrSignCount) || uk != nil {
		t.Fatalf("regression: %v %v", uk, err)
	}
	// An equal counter is refused too.
	p.dev.NextSignCount = &five
	if _, err := p.unlock(t, p.dev, nil); !errors.Is(err, ErrSignCount) {
		t.Fatalf("equal counter: %v", err)
	}
	p.st.DB.QueryRow(`SELECT sign_count FROM webauthn_credentials WHERE user_id=$1`, p.user).Scan(&sc)
	if sc != 5 {
		t.Fatalf("sign_count moved to %d on a refused assertion", sc)
	}
}

func TestCeremonyIsSingleUse(t *testing.T) {
	p := newPK(t)
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	ctx := context.Background()
	c, _ := p.st.BeginUnlock(ctx, p.user)
	a, prf := p.dev.Get(t, c.Options)
	if uk, err := p.st.FinishUnlock(ctx, p.user, c.ID, a, prf); err != nil {
		t.Fatal(err)
	} else {
		uk.Zero()
	}
	if _, err := p.st.FinishUnlock(ctx, p.user, c.ID, a, prf); !errors.Is(err, ErrCeremony) {
		t.Fatalf("replayed ceremony: %v", err)
	}
	// Another user cannot use it either.
	other, _ := p.root.CreateUser(ctx, "g-other", "o@x.test", "O", true)
	c2, _ := p.st.BeginUnlock(ctx, p.user)
	a2, prf2 := p.dev.Get(t, c2.Options)
	if _, err := p.st.FinishUnlock(ctx, other.ID, c2.ID, a2, prf2); err == nil {
		t.Fatal("ceremony used by another user")
	}
}

func TestRemoveLastPasskeyNeedsRecovery(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	var credID string
	p.st.DB.QueryRow(`SELECT id FROM webauthn_credentials WHERE user_id=$1`, p.user).Scan(&credID)
	if err := p.st.RemovePasskey(ctx, p.user, credID, res.UK, ""); err == nil {
		t.Fatal("removed the last passkey without the recovery key")
	}
	if err := p.st.RemovePasskey(ctx, p.user, credID, res.UK, res.Recovery); err != nil {
		t.Fatal(err)
	}
	if n := p.count(t, `SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, p.user) +
		p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='passkey'`, p.user); n != 0 {
		t.Fatal("passkey rows left after removal")
	}
	// The recovery key still unlocks.
	uk, err := p.st.UnlockWithRecovery(ctx, p.user, res.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	uk.Zero()
}

func TestAddPasskeyThenRemoveOneNeedsNoRecovery(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	second := webauthntest.New(t, pkRPID, pkOrigin)
	c, err := p.st.BeginAddPasskey(ctx, p.user)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.st.AddPasskey(ctx, p.user, res.UK, c.ID, second.Create(t, c.Options), second.PRFFromCreate(t, c.Options), "Phone"); err != nil {
		t.Fatal(err)
	}
	uk, err := p.unlock(t, second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !uk.Equal(res.UK) {
		t.Fatal("second passkey unwraps a different key")
	}
	uk.Zero()
	var credID string
	p.st.DB.QueryRow(`SELECT id FROM webauthn_credentials WHERE user_id=$1 ORDER BY created_at LIMIT 1`, p.user).Scan(&credID)
	if err := p.st.RemovePasskey(ctx, p.user, credID, res.UK, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRegenerateRecoveryInvalidatesOld(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	fresh, err := p.st.RegenerateRecovery(ctx, p.user, res.UK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.st.UnlockWithRecovery(ctx, p.user, res.Recovery); err == nil {
		t.Fatal("old recovery key still works")
	}
	uk, err := p.st.UnlockWithRecovery(ctx, p.user, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if !uk.Equal(res.UK) {
		t.Fatal("new recovery key unwraps a different key")
	}
	uk.Zero()
	if n := p.count(t, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='recovery'`, p.user); n != 1 {
		t.Fatalf("%d recovery rows, want 1", n)
	}
}

func TestEnrollmentDeletesBootstrapToken(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	if err := seedTunnelToken(ctx, p.st); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.st.ResolveBearer(ctx, "tunnel-token"); err != nil {
		t.Fatal(err)
	}
	p.user = cortex.BootstrapUserID
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	if n := p.count(t, `SELECT count(*) FROM agent_tokens`); n != 0 {
		t.Fatalf("%d agent tokens left after enrollment", n)
	}
	if _, _, _, err := p.st.ResolveBearer(ctx, "tunnel-token"); err == nil {
		t.Fatal("static token still resolves after enrollment")
	}
	// Other users' enrollment leaves the bootstrap token alone (checked in
	// the inverse test below).
}

func TestStaticTokenRejectedForEncryptedUser(t *testing.T) {
	p := newPK(t)
	ctx := context.Background()
	if err := seedTunnelToken(ctx, p.st); err != nil {
		t.Fatal(err)
	}
	p.st.SetStaticToken("tunnel-token")
	p.user = cortex.BootstrapUserID
	res, _, _ := p.enrollAndConfirm(t)
	defer res.UK.Zero()
	if _, _, _, err := p.st.ResolveBearer(ctx, "tunnel-token"); !errors.Is(err, model.ErrStaticTokenDisabled) {
		t.Fatalf("got %v, want ErrStaticTokenDisabled", err)
	}
	if _, _, _, err := p.st.ResolveBearer(ctx, "some-other-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unrelated token: got %v", err)
	}
}

// seedTunnelToken gives the bootstrap user the static owner token the retired
// environment seeding used to create.
func seedTunnelToken(ctx context.Context, st *Store) error {
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	conn, err := st.createConnection(ctx, tx, cortex.BootstrapUserID, "agent", tunnelTokenName, "")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens (id, connection_id, token_hash) VALUES ('t_static',$1,$2)`,
		conn.ID, HashToken("tunnel-token")); err != nil {
		return err
	}
	return tx.Commit()
}
