package accounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/store"
)

// Passkey enrollment and unlock.
//
// Enrollment is staged. FinishEnroll stores the passkey and recovery wraps
// with pending=true and leaves encryption_state at 'none'. Only ConfirmEnroll
// flips the state, and only after a fresh assertion plus PRF output unwraps
// the pending wrap to the same UK and the user has retyped two groups of the
// recovery key. A pending enrollment expires after PendingTTL and leaves the
// account exactly as it was.
//
// Nothing here logs or returns PRF output, recovery keys, wrapped bytes,
// challenges or request bodies. Errors are fixed text.

const (
	// PendingTTL is how long an unconfirmed enrollment lives.
	PendingTTL = time.Hour
	// CeremonyTTL is how long a WebAuthn challenge lives.
	CeremonyTTL = 5 * time.Minute
	// MigrateEncKind is the job kind of the owner's encryption migration.
	// ConfirmEnroll parks one; Task 16 runs it. StatusParked jobs are not
	// picked up by the pool.
	MigrateEncKind = "encrypt_migrate"
	StatusParked   = "parked"

	maxConfirmAttempts = 5
	recoveryRef        = "recovery"
	prfLen             = 32
)

// Fixed-text errors. None carries secret or user content.
var (
	ErrEnrollDisabled  = errors.New("passkey protection is not available yet")
	ErrNoPRF           = errors.New("this device cannot protect your memory; use a passkey that supports PRF")
	ErrCeremony        = errors.New("that step expired; start again")
	ErrNotEnrolled     = errors.New("this account is not protected by a passkey")
	ErrAlreadyEnrolled = errors.New("this account is already protected by a passkey")
	ErrPasskeyVerify   = errors.New("the passkey could not be verified")
	ErrSignCount       = errors.New("the passkey looks cloned and was refused")
	ErrProof           = errors.New("the passkey or recovery key groups did not match")
	ErrBadRecovery     = errors.New("that recovery key is not valid")
)

// PasskeyConfig configures WebAuthn for the store.
type PasskeyConfig struct {
	RPID, RPName  string
	Origins       []string
	EnrollEnabled bool // Config.EncryptionEnroll
}

// ConfigurePasskeys prepares WebAuthn. Call once at startup, before serving.
func (st *Store) ConfigurePasskeys(c PasskeyConfig) error {
	if c.RPName == "" {
		c.RPName = "cortex"
	}
	tc := webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL, TimeoutUVD: CeremonyTTL}
	wa, err := webauthn.New(&webauthn.Config{
		RPID: c.RPID, RPDisplayName: c.RPName, RPOrigins: c.Origins,
		Timeouts: webauthn.TimeoutsConfig{Login: tc, Registration: tc},
	})
	if err != nil {
		return err
	}
	st.wa, st.enrollOn = wa, c.EnrollEnabled
	return nil
}

// EnrollEnabled reports whether first-time enrollment may begin.
func (st *Store) EnrollEnabled() bool { return st.enrollOn && st.wa != nil }

// Ceremony is a begun WebAuthn step: the browser options and the id to send back.
type Ceremony struct {
	ID      string
	Options json.RawMessage
	// Positions are the 1-based recovery-key groups to retype (confirm only).
	Positions [2]int
}

// EnrollResult is what FinishEnroll returns. Recovery is shown once. The
// caller must Zero UK.
type EnrollResult struct {
	Recovery  string
	ConfirmID string
	Positions [2]int // 1-based groups of Recovery to retype
	UK        *keys.UK
}

type dbx interface {
	execer
	rower
}

// --- users and credentials ---

type pkUser struct {
	id    []byte
	name  string
	creds []webauthn.Credential
}

func (u *pkUser) WebAuthnID() []byte                         { return u.id }
func (u *pkUser) WebAuthnName() string                       { return u.name }
func (u *pkUser) WebAuthnDisplayName() string                { return u.name }
func (u *pkUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

type credRow struct {
	ID      string // webauthn_credentials.id (also the wrap ref_id)
	Cred    webauthn.Credential
	PRFSalt []byte
}

func (st *Store) userState(ctx context.Context, x rower, userID string) (state, name string, err error) {
	var n, e string
	err = x.QueryRowContext(ctx,
		`SELECT encryption_state, COALESCE(name,''), COALESCE(email,'') FROM users WHERE id = $1 AND deleted_at IS NULL`,
		userID).Scan(&state, &n, &e)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if n == "" {
		n = e
	}
	if n == "" {
		n = userID
	}
	return state, n, err
}

// loadCreds returns the user's credentials whose passkey wrap has the given
// pending state and has not expired.
func (st *Store) loadCreds(ctx context.Context, userID string, pending bool) ([]credRow, error) {
	rows, err := st.DB.QueryContext(ctx,
		`SELECT c.id, c.credential_id, c.public_key, c.sign_count, COALESCE(array_to_string(c.transports, ','), ''), c.prf_salt
		 FROM webauthn_credentials c
		 JOIN user_key_wraps w ON w.user_id = c.user_id AND w.kind = 'passkey' AND w.ref_id = c.id
		 WHERE c.user_id = $1 AND w.pending = $2 AND w.revoked_at IS NULL AND (w.expires_at IS NULL OR w.expires_at > $3)
		 ORDER BY c.created_at, c.id`, userID, pending, st.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []credRow
	for rows.Next() {
		var r credRow
		var sc int64
		var tr string
		if err := rows.Scan(&r.ID, &r.Cred.ID, &r.Cred.PublicKey, &sc, &tr, &r.PRFSalt); err != nil {
			return nil, err
		}
		r.Cred.Authenticator.SignCount = uint32(sc)
		for _, t := range strings.Split(tr, ",") {
			if t != "" {
				r.Cred.Transport = append(r.Cred.Transport, protocol.AuthenticatorTransport(t))
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func credUser(userID, name string, rows []credRow) *pkUser {
	u := &pkUser{id: []byte(userID), name: name}
	for _, r := range rows {
		u.creds = append(u.creds, r.Cred)
	}
	return u
}

func descriptors(rows []credRow) []protocol.CredentialDescriptor {
	var d []protocol.CredentialDescriptor
	for _, r := range rows {
		d = append(d, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: r.Cred.ID, Transport: r.Cred.Transport})
	}
	return d
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// --- ceremonies ---

func (st *Store) putCeremony(ctx context.Context, x execer, userID, kind string, data any, ttl time.Duration) (string, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	id := randHex(24)
	_, err = x.ExecContext(ctx, `INSERT INTO webauthn_ceremonies (id_hash, user_id, kind, data, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		HashToken(id), userID, kind, b, st.now().Add(ttl))
	return id, err
}

// takeCeremony loads and deletes a single-use ceremony.
func (st *Store) takeCeremony(ctx context.Context, userID, kind, id string, into any) error {
	var raw []byte
	err := st.DB.QueryRowContext(ctx,
		`DELETE FROM webauthn_ceremonies WHERE id_hash = $1 AND user_id = $2 AND kind = $3 AND expires_at > $4 RETURNING data`,
		HashToken(id), userID, kind, st.now()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCeremony
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (st *Store) peekCeremony(ctx context.Context, userID, kind, id string, into any) error {
	var raw []byte
	err := st.DB.QueryRowContext(ctx,
		`SELECT data FROM webauthn_ceremonies WHERE id_hash = $1 AND user_id = $2 AND kind = $3 AND expires_at > $4`,
		HashToken(id), userID, kind, st.now()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCeremony
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (st *Store) saveCeremony(ctx context.Context, id string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = st.DB.ExecContext(ctx, `UPDATE webauthn_ceremonies SET data = $2 WHERE id_hash = $1`, HashToken(id), b)
	return err
}

func (st *Store) dropCeremony(ctx context.Context, id string) {
	_, _ = st.DB.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE id_hash = $1`, HashToken(id))
}

type regCeremony struct {
	Session webauthn.SessionData `json:"s"`
	PRFSalt []byte               `json:"salt"`
}

type loginCeremony struct {
	Session webauthn.SessionData `json:"s"`
}

type confirmCeremony struct {
	CredID    string                `json:"cred"`
	Sealed    []byte                `json:"sealed"` // expected groups, sealed under UK
	Positions [2]int                `json:"pos"`
	Session   *webauthn.SessionData `json:"s,omitempty"`
	Attempts  int                   `json:"n"`
}

// --- begin registration ---

// BeginEnroll starts first-time enrollment. It refuses unless the enrollment
// flag is on and the account is not yet protected.
func (st *Store) BeginEnroll(ctx context.Context, userID string) (*Ceremony, error) {
	if !st.EnrollEnabled() {
		return nil, ErrEnrollDisabled
	}
	return st.beginRegistration(ctx, userID, true)
}

// BeginAddPasskey starts registering another passkey for an enrolled account.
func (st *Store) BeginAddPasskey(ctx context.Context, userID string) (*Ceremony, error) {
	if st.wa == nil {
		return nil, ErrEnrollDisabled
	}
	return st.beginRegistration(ctx, userID, false)
}

func (st *Store) beginRegistration(ctx context.Context, userID string, first bool) (*Ceremony, error) {
	state, name, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return nil, err
	}
	if first && state != "none" {
		return nil, ErrAlreadyEnrolled
	}
	if !first && state == "none" {
		return nil, ErrNotEnrolled
	}
	var existing []credRow
	if !first {
		if existing, err = st.loadCreds(ctx, userID, false); err != nil {
			return nil, err
		}
	}
	salt := make([]byte, prfLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	rk := protocol.ResidentKeyRequirementPreferred
	creation, session, err := st.wa.BeginRegistration(credUser(userID, name, existing),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: rk, UserVerification: protocol.VerificationRequired}),
		webauthn.WithExclusions(descriptors(existing)),
		webauthn.WithExtensions(webauthn.WithExtensionPRF(protocol.PRFValues{First: salt})))
	if err != nil {
		return nil, ErrPasskeyVerify
	}
	opts, err := json.Marshal(creation)
	if err != nil {
		return nil, err
	}
	id, err := st.putCeremony(ctx, st.DB, userID, "enroll", regCeremony{Session: *session, PRFSalt: salt}, CeremonyTTL)
	if err != nil {
		return nil, err
	}
	return &Ceremony{ID: id, Options: opts}, nil
}

// verifyAttestation checks the registration response against the ceremony.
func (st *Store) verifyAttestation(ctx context.Context, userID, ceremonyID string, attestation []byte) (*webauthn.Credential, *regCeremony, error) {
	var rc regCeremony
	if err := st.takeCeremony(ctx, userID, "enroll", ceremonyID, &rc); err != nil {
		return nil, nil, err
	}
	state, name, err := st.userState(ctx, st.DB, userID)
	_ = state
	if err != nil {
		return nil, nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(attestation)
	if err != nil {
		return nil, nil, ErrPasskeyVerify
	}
	cred, err := st.wa.CreateCredential(credUser(userID, name, nil), rc.Session, parsed)
	if err != nil {
		return nil, nil, ErrPasskeyVerify
	}
	return cred, &rc, nil
}

func insertCred(ctx context.Context, x execer, userID, id string, c *webauthn.Credential, salt []byte, name string) error {
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	var tr []string
	for _, t := range c.Transport {
		tr = append(tr, string(t))
	}
	_, err := x.ExecContext(ctx,
		`INSERT INTO webauthn_credentials (id, user_id, credential_id, public_key, sign_count, transports, prf_salt, name)
		 VALUES ($1,$2,$3,$4,$5,string_to_array(NULLIF($6,''), ','),$7,$8)`,
		id, userID, c.ID, c.PublicKey, int64(c.Authenticator.SignCount), strings.Join(tr, ","), salt, name)
	return err
}

// FinishEnroll verifies the new passkey and stores pending wraps. The account
// stays 'none' until ConfirmEnroll. prf is the 32-byte PRF output the browser
// obtained for the ceremony's salt.
func (st *Store) FinishEnroll(ctx context.Context, userID, ceremonyID string, attestation, prf []byte, name string) (*EnrollResult, error) {
	if !st.EnrollEnabled() {
		return nil, ErrEnrollDisabled
	}
	if len(prf) != prfLen {
		return nil, ErrNoPRF
	}
	cred, rc, err := st.verifyAttestation(ctx, userID, ceremonyID, attestation)
	if err != nil {
		return nil, err
	}
	uk, err := keys.NewUK()
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			uk.Zero()
		}
	}()
	rawRec, display, err := keys.NewRecoveryKey()
	if err != nil {
		return nil, err
	}
	defer clear(rawRec)
	groups := strings.Split(display, "-")
	pos, err := pickGroups(len(groups) - 1) // the last group is short; never ask for it
	if err != nil {
		return nil, err
	}
	confirmID := randHex(24)
	sealed, err := uk.Seal(userID, "webauthn_ceremonies", HashToken(confirmID), "groups",
		[]byte(groups[pos[0]]+","+groups[pos[1]]))
	if err != nil {
		return nil, err
	}
	credID := "pk_" + randHex(12)
	pw, err := keys.Wrap(keys.WrapKeyPasskey(prf), userID, "passkey", credID, uk)
	if err != nil {
		return nil, err
	}
	rw, err := keys.Wrap(keys.WrapKeyRecovery(rawRec), userID, "recovery", recoveryRef, uk)
	if err != nil {
		return nil, err
	}
	now := st.now()
	exp := now.Add(PendingTTL)

	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT encryption_state FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&state); err != nil {
		return nil, ErrNotFound
	}
	if state != "none" {
		return nil, ErrAlreadyEnrolled
	}
	if err := deletePendingEnrollment(ctx, tx, userID); err != nil {
		return nil, err
	}
	if err := insertCred(ctx, tx, userID, credID, cred, rc.PRFSalt, name); err != nil {
		return nil, err
	}
	for _, w := range []Wrap{
		{UserID: userID, Kind: "passkey", RefID: credID, Wrapped: pw, Pending: true, ExpiresAt: &exp},
		{UserID: userID, Kind: "recovery", RefID: recoveryRef, Wrapped: rw, Pending: true, ExpiresAt: &exp},
	} {
		if err := putWrap(ctx, tx, w); err != nil {
			return nil, err
		}
	}
	b, _ := json.Marshal(confirmCeremony{CredID: credID, Sealed: sealed, Positions: pos})
	if _, err := tx.ExecContext(ctx, `INSERT INTO webauthn_ceremonies (id_hash, user_id, kind, data, expires_at) VALUES ($1,$2,'confirm',$3,$4)`,
		HashToken(confirmID), userID, b, exp); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ok = true
	return &EnrollResult{Recovery: display, ConfirmID: confirmID, Positions: [2]int{pos[0] + 1, pos[1] + 1}, UK: uk}, nil
}

// pickGroups returns two distinct random indices in [0, n).
func pickGroups(n int) ([2]int, error) {
	a, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return [2]int{}, err
	}
	b, err := rand.Int(rand.Reader, big.NewInt(int64(n-1)))
	if err != nil {
		return [2]int{}, err
	}
	i, j := int(a.Int64()), int(b.Int64())
	if j >= i {
		j++
	}
	if i > j {
		i, j = j, i
	}
	return [2]int{i, j}, nil
}

// deletePendingEnrollment removes every unconfirmed enrollment artefact of a
// user (credentials, pending wraps, confirm ceremonies).
func deletePendingEnrollment(ctx context.Context, x execer, userID string) error {
	for _, q := range []string{
		`DELETE FROM webauthn_credentials WHERE user_id = $1 AND id IN
		   (SELECT ref_id FROM user_key_wraps WHERE user_id = $1 AND kind = 'passkey' AND pending)`,
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND pending`,
		`DELETE FROM webauthn_ceremonies WHERE user_id = $1 AND kind = 'confirm'`,
	} {
		if _, err := x.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	return nil
}

// --- assertions ---

func (st *Store) beginAssertion(userID, name string, rows []credRow) (json.RawMessage, *webauthn.SessionData, error) {
	by := map[string]protocol.PRFValues{}
	for _, r := range rows {
		by[b64u(r.Cred.ID)] = protocol.PRFValues{First: r.PRFSalt}
	}
	assertion, session, err := st.wa.BeginLogin(credUser(userID, name, rows),
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAssertionExtensions(webauthn.WithExtensionPRFByCredential(by, nil)))
	if err != nil {
		return nil, nil, ErrPasskeyVerify
	}
	opts, err := json.Marshal(assertion)
	if err != nil {
		return nil, nil, err
	}
	return opts, session, nil
}

// verifyAssertion validates a login response and returns the matching
// credential row. It refuses a sign-count regression.
func (st *Store) verifyAssertion(userID, name string, rows []credRow, session webauthn.SessionData, assertion []byte) (*credRow, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(assertion)
	if err != nil {
		return nil, nil, ErrPasskeyVerify
	}
	// The credentials table does not store the backup-eligible flag, so take it
	// from the response (it is not a security signal here).
	u := credUser(userID, name, rows)
	var match *credRow
	for i := range rows {
		if bytes.Equal(rows[i].Cred.ID, parsed.RawID) {
			u.creds[i].Flags.BackupEligible = parsed.Response.AuthenticatorData.Flags.HasBackupEligible()
			match = &rows[i]
		}
	}
	if match == nil {
		return nil, nil, ErrPasskeyVerify
	}
	cred, err := st.wa.ValidateLogin(u, session, parsed)
	if err != nil {
		return nil, nil, ErrPasskeyVerify
	}
	if cred.Authenticator.CloneWarning {
		return nil, nil, ErrSignCount
	}
	return match, cred, nil
}

func (st *Store) touchCred(ctx context.Context, id string, c *webauthn.Credential) {
	_, _ = st.DB.ExecContext(ctx, `UPDATE webauthn_credentials SET sign_count = $2, last_used_at = $3 WHERE id = $1`,
		id, int64(c.Authenticator.SignCount), st.now())
}

// --- confirm ---

// BeginConfirm starts the proof round: a fresh assertion for the pending
// passkey. It also returns which recovery groups the user must retype.
func (st *Store) BeginConfirm(ctx context.Context, userID, confirmID string) (*Ceremony, error) {
	if !st.EnrollEnabled() {
		return nil, ErrEnrollDisabled
	}
	var cc confirmCeremony
	if err := st.peekCeremony(ctx, userID, "confirm", confirmID, &cc); err != nil {
		return nil, err
	}
	state, name, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return nil, err
	}
	if state != "none" {
		return nil, ErrAlreadyEnrolled
	}
	rows, err := st.loadCreds(ctx, userID, true)
	if err != nil || len(rows) == 0 {
		return nil, ErrCeremony
	}
	opts, session, err := st.beginAssertion(userID, name, rows)
	if err != nil {
		return nil, err
	}
	cc.Session = session
	if err := st.saveCeremony(ctx, confirmID, cc); err != nil {
		return nil, err
	}
	return &Ceremony{ID: confirmID, Options: opts, Positions: [2]int{cc.Positions[0] + 1, cc.Positions[1] + 1}}, nil
}

func normGroup(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(s))
}

// ConfirmEnroll completes enrollment. It requires a fresh assertion and PRF
// output that unwraps the pending passkey wrap to the UK, plus the two
// retyped recovery groups. Only then does the state change. On success it
// returns the new state ('encrypted' or 'migrating') and the UK; the caller
// must Zero it. Any failure leaves the account at 'none'.
func (st *Store) ConfirmEnroll(ctx context.Context, userID, confirmID string, assertion, prf []byte, groups []string) (string, *keys.UK, error) {
	if !st.EnrollEnabled() {
		return "", nil, ErrEnrollDisabled
	}
	if len(prf) != prfLen {
		return "", nil, ErrNoPRF
	}
	var cc confirmCeremony
	if err := st.peekCeremony(ctx, userID, "confirm", confirmID, &cc); err != nil {
		return "", nil, err
	}
	if cc.Session == nil || cc.Attempts >= maxConfirmAttempts {
		return "", nil, ErrCeremony
	}
	// The challenge is single-use; a retry needs a new BeginConfirm.
	session := *cc.Session
	cc.Session = nil
	cc.Attempts++
	fail := func(err error) (string, *keys.UK, error) {
		if cc.Attempts >= maxConfirmAttempts {
			st.dropCeremony(ctx, confirmID)
		} else {
			_ = st.saveCeremony(ctx, confirmID, cc)
		}
		return "", nil, err
	}
	state, name, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return "", nil, err
	}
	if state != "none" {
		return "", nil, ErrAlreadyEnrolled
	}
	rows, err := st.loadCreds(ctx, userID, true)
	if err != nil || len(rows) == 0 {
		return "", nil, ErrCeremony
	}
	match, cred, err := st.verifyAssertion(userID, name, rows, session, assertion)
	if err != nil {
		return fail(err)
	}
	if match.ID != cc.CredID {
		return fail(ErrProof)
	}
	w, err := st.GetWrap(ctx, userID, "passkey", match.ID)
	if err != nil || !w.Pending {
		return "", nil, ErrCeremony
	}
	uk, err := keys.Unwrap(keys.WrapKeyPasskey(prf), userID, "passkey", match.ID, w.Wrapped)
	if err != nil {
		return fail(ErrProof)
	}
	done := false
	defer func() {
		if !done {
			uk.Zero()
		}
	}()
	plain, err := uk.Open(userID, "webauthn_ceremonies", HashToken(confirmID), "groups", cc.Sealed)
	if err != nil || len(groups) != 2 {
		return fail(ErrProof)
	}
	want := strings.Split(string(plain), ",")
	if len(want) != 2 ||
		subtle.ConstantTimeCompare([]byte(normGroup(groups[0])), []byte(want[0])) != 1 ||
		subtle.ConstantTimeCompare([]byte(normGroup(groups[1])), []byte(want[1])) != 1 {
		return fail(ErrProof)
	}

	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	var cur string
	if err := tx.QueryRowContext(ctx, `SELECT encryption_state FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&cur); err != nil {
		return "", nil, ErrNotFound
	}
	if cur != "none" {
		return "", nil, ErrAlreadyEnrolled
	}
	// Still pending and unexpired inside the transaction.
	res, err := tx.ExecContext(ctx,
		`UPDATE user_key_wraps SET pending = FALSE, expires_at = NULL, created_at = now()
		 WHERE user_id = $1 AND pending AND ((kind = 'passkey' AND ref_id = $2) OR (kind = 'recovery' AND ref_id = $3))
		   AND (expires_at IS NULL OR expires_at > $4)`, userID, match.ID, recoveryRef, st.now())
	if err != nil {
		return "", nil, err
	}
	if n, _ := res.RowsAffected(); n != 2 {
		return "", nil, ErrCeremony
	}
	var legacy bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM memories WHERE user_id = $1)
		     OR EXISTS (SELECT 1 FROM chunks WHERE user_id = $1)
		     OR EXISTS (SELECT 1 FROM relationships WHERE user_id = $1)
		     OR EXISTS (SELECT 1 FROM entities WHERE user_id = $1 AND NOT is_owner)`, userID).Scan(&legacy); err != nil {
		return "", nil, err
	}
	next := "encrypted"
	if legacy {
		next = "migrating"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET encryption_state = $2 WHERE id = $1`, userID, next); err != nil {
		return "", nil, err
	}
	if next == "migrating" {
		// Parked until a key-bearing request attaches (Tasks 14 and 16).
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (id, user_id, kind, status) VALUES ($1,$2,$3,$4)`,
			"j_"+randHex(12), userID, MigrateEncKind, StatusParked); err != nil {
			return "", nil, err
		}
	}
	if next == "encrypted" {
		// A new account has no data yet except the owner entity that sign-up
		// wrote in plaintext. Nothing else would encrypt it (the migration
		// job only runs for 'migrating' accounts), and the row-form guard
		// rejects a plaintext row in an encrypted account.
		if err := sealOwnerEntity(ctx, tx, uk, userID); err != nil {
			return "", nil, err
		}
	}
	// Tokens issued before encryption carry no key wrap: flag them so
	// Connections offers Reconnect and the MCP error says what to do.
	if err := markNeedsReconnect(ctx, tx, userID); err != nil {
		return "", nil, err
	}
	if userID == cortex.BootstrapUserID {
		// The static owner token would amount to a server-held key.
		if _, err := tx.ExecContext(ctx, `DELETE FROM connections WHERE user_id = $1 AND kind = 'agent' AND name = $2`,
			userID, tunnelTokenName); err != nil {
			return "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE user_id = $1 AND kind IN ('confirm','enroll')`, userID); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	done = true
	st.touchCred(ctx, match.ID, cred)
	return next, uk, nil
}

// --- unlock ---

// BeginUnlock starts a passkey unlock for an enrolled account.
func (st *Store) BeginUnlock(ctx context.Context, userID string) (*Ceremony, error) {
	if st.wa == nil {
		return nil, ErrEnrollDisabled
	}
	state, name, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return nil, err
	}
	if state == "none" {
		return nil, ErrNotEnrolled
	}
	rows, err := st.loadCreds(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotEnrolled
	}
	opts, session, err := st.beginAssertion(userID, name, rows)
	if err != nil {
		return nil, err
	}
	id, err := st.putCeremony(ctx, st.DB, userID, "unlock", loginCeremony{Session: *session}, CeremonyTTL)
	if err != nil {
		return nil, err
	}
	return &Ceremony{ID: id, Options: opts}, nil
}

// FinishUnlock verifies the assertion and unwraps the UK with the PRF output.
// A valid assertion with the wrong PRF output yields keys.ErrDecrypt. The
// caller must Zero the returned UK.
func (st *Store) FinishUnlock(ctx context.Context, userID, ceremonyID string, assertion, prf []byte) (*keys.UK, error) {
	if st.wa == nil {
		return nil, ErrEnrollDisabled
	}
	var lc loginCeremony
	if err := st.takeCeremony(ctx, userID, "unlock", ceremonyID, &lc); err != nil {
		return nil, err
	}
	state, name, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return nil, err
	}
	if state == "none" {
		return nil, ErrNotEnrolled
	}
	rows, err := st.loadCreds(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	match, cred, err := st.verifyAssertion(userID, name, rows, lc.Session, assertion)
	if err != nil {
		return nil, err
	}
	st.touchCred(ctx, match.ID, cred)
	if len(prf) != prfLen {
		return nil, keys.ErrDecrypt
	}
	w, err := st.GetWrap(ctx, userID, "passkey", match.ID)
	if err != nil || w.Pending {
		return nil, keys.ErrDecrypt
	}
	return keys.Unwrap(keys.WrapKeyPasskey(prf), userID, "passkey", match.ID, w.Wrapped)
}

// UnlockWithRecovery unwraps the UK with the recovery key.
func (st *Store) UnlockWithRecovery(ctx context.Context, userID, recovery string) (*keys.UK, error) {
	state, _, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return nil, err
	}
	if state == "none" {
		return nil, ErrNotEnrolled
	}
	raw, err := keys.ParseRecoveryKey(recovery)
	if err != nil {
		return nil, ErrBadRecovery
	}
	defer clear(raw)
	w, err := st.GetWrap(ctx, userID, "recovery", recoveryRef)
	if err != nil || w.Pending {
		return nil, ErrBadRecovery
	}
	uk, err := keys.Unwrap(keys.WrapKeyRecovery(raw), userID, "recovery", recoveryRef, w.Wrapped)
	if err != nil {
		return nil, ErrBadRecovery
	}
	return uk, nil
}

// --- passkey management ---

// AddPasskey registers another passkey for an enrolled account. It needs the
// unlocked UK; the new passkey's wrap is live at once.
func (st *Store) AddPasskey(ctx context.Context, userID string, uk *keys.UK, ceremonyID string, attestation, prf []byte, name string) error {
	if st.wa == nil {
		return ErrEnrollDisabled
	}
	if uk == nil {
		return ErrNotEnrolled
	}
	if len(prf) != prfLen {
		return ErrNoPRF
	}
	state, _, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return err
	}
	if state == "none" {
		return ErrNotEnrolled
	}
	cred, rc, err := st.verifyAttestation(ctx, userID, ceremonyID, attestation)
	if err != nil {
		return err
	}
	credID := "pk_" + randHex(12)
	pw, err := keys.Wrap(keys.WrapKeyPasskey(prf), userID, "passkey", credID, uk)
	if err != nil {
		return err
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertCred(ctx, tx, userID, credID, cred, rc.PRFSalt, name); err != nil {
		return err
	}
	if err := putWrap(ctx, tx, Wrap{UserID: userID, Kind: "passkey", RefID: credID, Wrapped: pw}); err != nil {
		return err
	}
	return tx.Commit()
}

// RemovePasskey deletes a passkey and its wrap. Removing the last one needs
// the recovery key to verify.
func (st *Store) RemovePasskey(ctx context.Context, userID, credID string, uk *keys.UK, recovery string) error {
	if uk == nil {
		return ErrNotEnrolled
	}
	rows, err := st.loadCreds(ctx, userID, false)
	if err != nil {
		return err
	}
	found := false
	for _, r := range rows {
		found = found || r.ID == credID
	}
	if !found {
		return ErrNotFound
	}
	if len(rows) == 1 {
		rk, err := st.UnlockWithRecovery(ctx, userID, recovery)
		if err != nil {
			return err
		}
		rk.Zero()
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteWrap(ctx, tx, userID, "passkey", credID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = $1 AND user_id = $2`, credID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// RegenerateRecovery replaces the recovery key. The old one stops working.
// It needs the unlocked UK and returns the new key's display form.
func (st *Store) RegenerateRecovery(ctx context.Context, userID string, uk *keys.UK) (string, error) {
	if uk == nil {
		return "", ErrNotEnrolled
	}
	state, _, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return "", err
	}
	if state == "none" {
		return "", ErrNotEnrolled
	}
	raw, display, err := keys.NewRecoveryKey()
	if err != nil {
		return "", err
	}
	defer clear(raw)
	w, err := keys.Wrap(keys.WrapKeyRecovery(raw), userID, "recovery", recoveryRef, uk)
	if err != nil {
		return "", err
	}
	if err := st.PutWrap(ctx, Wrap{UserID: userID, Kind: "recovery", RefID: recoveryRef, Wrapped: w}); err != nil {
		return "", err
	}
	return display, nil
}

// PurgeEnrollments deletes expired ceremonies, expired wraps (including
// pending enrollment wraps) and credentials left without a wrap.
func (st *Store) PurgeEnrollments(ctx context.Context, now time.Time) error {
	if _, err := st.PurgeExpiredWraps(ctx, now); err != nil {
		return err
	}
	if _, err := st.DB.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE expires_at <= $1`, now); err != nil {
		return err
	}
	_, err := st.DB.ExecContext(ctx,
		`DELETE FROM webauthn_credentials c WHERE NOT EXISTS
		   (SELECT 1 FROM user_key_wraps w WHERE w.user_id = c.user_id AND w.kind = 'passkey' AND w.ref_id = c.id)`)
	return err
}

// sealOwnerEntity rewrites the user's plaintext owner entity as encrypted:
// the same shape graph.writeEntityEnc produces (ciphertext columns, blind
// indexes on normalised name and type, plain columns cleared).
func sealOwnerEntity(ctx context.Context, tx *sql.Tx, uk *keys.UK, userID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(name,''), COALESCE(type,''), COALESCE(source,''), COALESCE(attributes::text,'{}')
		 FROM entities WHERE user_id = $1 AND is_owner AND enc IS NOT TRUE FOR UPDATE`, userID)
	if err != nil {
		return err
	}
	type row struct{ id, name, typ, source, attrs string }
	var owners []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.typ, &r.source, &r.attrs); err != nil {
			rows.Close()
			return err
		}
		owners = append(owners, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range owners {
		seal := func(col, v string) ([]byte, error) { return uk.Seal(userID, store.TblEntities, r.id, col, []byte(v)) }
		nameCT, err := seal("name", r.name)
		if err != nil {
			return err
		}
		typeCT, err := seal("type", r.typ)
		if err != nil {
			return err
		}
		sourceCT, err := seal("source", r.source)
		if err != nil {
			return err
		}
		attrsCT, err := seal("attributes", r.attrs)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entities SET enc = TRUE, name = NULL, type = NULL, source = NULL, attributes = NULL,
			   name_ct = $3, type_ct = $4, source_ct = $5, attributes_ct = $6, name_blind = $7, type_blind = $8
			 WHERE id = $1 AND user_id = $2`,
			r.id, userID, nameCT, typeCT, sourceCT, attrsCT,
			uk.Blind(store.NormName(r.name)), uk.Blind(store.NormName(r.typ))); err != nil {
			return err
		}
	}
	return nil
}
