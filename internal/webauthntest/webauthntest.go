// Package webauthntest is a minimal software WebAuthn authenticator for
// tests. It creates "none"-format attestations and signs assertions with an
// ECDSA P-256 key, and models the PRF extension with a per-credential secret.
// It is for tests only and holds no real security properties.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"testing"
)

// Authenticator is one software credential.
type Authenticator struct {
	RPID      string
	Origin    string
	CredID    []byte
	SignCount uint32
	// Counter is added to SignCount after each assertion; tests may set a
	// fixed value through NextSignCount instead.
	NextSignCount *uint32
	prfSecret     []byte
	key           *ecdsa.PrivateKey
	UserHandle    []byte
}

var b64 = base64.RawURLEncoding

// New returns an authenticator bound to rpID and origin.
func New(t testing.TB, rpID, origin string) *Authenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &Authenticator{RPID: rpID, Origin: origin, key: k, CredID: make([]byte, 32), prfSecret: make([]byte, 32)}
	_, _ = rand.Read(a.CredID)
	_, _ = rand.Read(a.prfSecret)
	return a
}

// PRF returns the PRF output for salt, as a browser would after hashing.
func (a *Authenticator) PRF(salt []byte) []byte {
	m := hmac.New(sha256.New, a.prfSecret)
	m.Write(salt)
	return m.Sum(nil)
}

// --- minimal CBOR encoder (just what an attestation needs) ---

func head(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	default:
		return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
	}
}

func cInt(v int64) []byte {
	if v >= 0 {
		return head(0, uint64(v))
	}
	return head(1, uint64(-1-v))
}
func cBytes(b []byte) []byte { return append(head(2, uint64(len(b))), b...) }
func cText(s string) []byte  { return append(head(3, uint64(len(s))), s...) }
func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func (a *Authenticator) coseKey() []byte {
	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))
	return cat(head(5, 5),
		cInt(1), cInt(2), // kty EC2
		cInt(3), cInt(-7), // alg ES256
		cInt(-1), cInt(1), // crv P-256
		cInt(-2), cBytes(x),
		cInt(-3), cBytes(y))
}

func (a *Authenticator) rpHash() []byte {
	h := sha256.Sum256([]byte(a.RPID))
	return h[:]
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

type options struct {
	PublicKey struct {
		Challenge  string `json:"challenge"`
		Extensions struct {
			PRF struct {
				Eval struct {
					First string `json:"first"`
				} `json:"eval"`
				EvalByCredential map[string]struct {
					First string `json:"first"`
				} `json:"evalByCredential"`
			} `json:"prf"`
		} `json:"extensions"`
	} `json:"publicKey"`
}

func parseOptions(t testing.TB, optionsJSON []byte) options {
	t.Helper()
	var o options
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

// Create answers a credential-creation options document with a registration
// response in the browser's JSON form.
func (a *Authenticator) Create(t testing.TB, optionsJSON []byte) []byte {
	t.Helper()
	o := parseOptions(t, optionsJSON)
	authData := cat(a.rpHash(), []byte{0x45}, make([]byte, 4), make([]byte, 16),
		[]byte{byte(len(a.CredID) >> 8), byte(len(a.CredID))}, a.CredID, a.coseKey())
	att := cat(head(5, 3), cText("fmt"), cText("none"), cText("attStmt"), head(5, 0), cText("authData"), cBytes(authData))
	cd := clientData("webauthn.create", o.PublicKey.Challenge, a.Origin)
	resp := map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	}
	b, _ := json.Marshal(resp)
	return b
}

// Get answers an assertion options document. It returns the assertion in the
// browser's JSON form and the PRF output for the salt the options carried for
// this credential (nil if none).
func (a *Authenticator) Get(t testing.TB, optionsJSON []byte) (assertion, prf []byte) {
	t.Helper()
	o := parseOptions(t, optionsJSON)
	count := a.SignCount + 1
	if a.NextSignCount != nil {
		count = *a.NextSignCount
	}
	a.SignCount = count
	cnt := make([]byte, 4)
	binary.BigEndian.PutUint32(cnt, count)
	authData := cat(a.rpHash(), []byte{0x05}, cnt)
	cd := clientData("webauthn.get", o.PublicKey.Challenge, a.Origin)
	cdh := sha256.Sum256(cd)
	digest := sha256.Sum256(cat(authData, cdh[:]))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := asn1.Marshal(struct{ R, S *big.Int }{r, s})
	resp := map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(authData),
			"signature":         b64.EncodeToString(sig),
		},
		"clientExtensionResults": map[string]any{},
	}
	if len(a.UserHandle) > 0 {
		resp["response"].(map[string]any)["userHandle"] = b64.EncodeToString(a.UserHandle)
	}
	if e, ok := o.PublicKey.Extensions.PRF.EvalByCredential[b64.EncodeToString(a.CredID)]; ok && e.First != "" {
		salt, _ := b64.DecodeString(e.First)
		prf = a.PRF(salt)
	} else if o.PublicKey.Extensions.PRF.Eval.First != "" {
		salt, _ := b64.DecodeString(o.PublicKey.Extensions.PRF.Eval.First)
		prf = a.PRF(salt)
	}
	b, _ := json.Marshal(resp)
	return b, prf
}

// PRFFromCreate returns the PRF output for the eval salt in creation options.
func (a *Authenticator) PRFFromCreate(t testing.TB, optionsJSON []byte) []byte {
	t.Helper()
	o := parseOptions(t, optionsJSON)
	salt, err := b64.DecodeString(o.PublicKey.Extensions.PRF.Eval.First)
	if err != nil || len(salt) == 0 {
		t.Fatal("creation options carry no prf salt")
	}
	return a.PRF(salt)
}
