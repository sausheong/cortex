package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

const (
	loginCookie   = "__Host-cortex_login"
	loginTTL      = 10 * time.Minute
	restoreWindow = 30 * 24 * time.Hour
)

func randToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Identity is what the identity provider asserts about a person.
type Identity struct {
	Sub, Email, Name string
	EmailVerified    bool
}

// IdentityProvider is the sign-in leg. Exchange must verify the ID token,
// including that its nonce equals nonce.
type IdentityProvider interface {
	AuthURL(state, nonce string) string
	Exchange(ctx context.Context, code, nonce string) (Identity, error)
}

type googleIdentity struct {
	cfg      *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func newGoogleIdentity(clientID, secret, redirect string) *googleIdentity {
	ks := oidc.NewRemoteKeySet(context.Background(), "https://www.googleapis.com/oauth2/v3/certs")
	return &googleIdentity{
		cfg: &oauth2.Config{
			ClientID: clientID, ClientSecret: secret, RedirectURL: redirect,
			Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token"}, Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
		},
		// Google issues both spellings; the issuer is checked by hand below.
		verifier: oidc.NewVerifier("https://accounts.google.com", ks, &oidc.Config{ClientID: clientID, SkipIssuerCheck: true}),
	}
}

func (g *googleIdentity) AuthURL(state, nonce string) string {
	return g.cfg.AuthCodeURL(state, oidc.Nonce(nonce))
}

func (g *googleIdentity) Exchange(ctx context.Context, code, nonce string) (Identity, error) {
	tok, err := g.cfg.Exchange(ctx, code)
	if err != nil {
		return Identity{}, errors.New("sign-in failed")
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := g.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, errors.New("sign-in failed")
	}
	if idt.Issuer != "https://accounts.google.com" && idt.Issuer != "accounts.google.com" {
		return Identity{}, errors.New("sign-in failed")
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return Identity{}, errors.New("sign-in failed")
	}
	var c struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idt.Claims(&c); err != nil {
		return Identity{}, errors.New("sign-in failed")
	}
	return Identity{Sub: idt.Subject, Email: c.Email, Name: c.Name, EmailVerified: c.EmailVerified}, nil
}

// safeReturnTo accepts only a same-origin path.
func safeReturnTo(next string) string {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

func (s *server) googleStart(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit.Allow(accounts.ClientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	id, state, nonce := randToken(), randToken(), randToken()
	now := s.now()
	if _, err := s.st.DB.ExecContext(r.Context(),
		`INSERT INTO login_attempts (id_hash, state, nonce, return_to, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		accounts.HashToken(id), state, nonce, safeReturnTo(r.URL.Query().Get("next")), now.Add(loginTTL)); err != nil {
		s.cfg.Logger.Error("login attempt", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: id, Path: "/", MaxAge: int(loginTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.idp.AuthURL(state, nonce), http.StatusFound)
}

func (s *server) googleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.authLimit.Allow(accounts.ClientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	ck, err := r.Cookie(loginCookie)
	if err != nil || ck.Value == "" {
		http.Error(w, "sign-in expired; start again", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	// Consume the attempt: single use, whatever happens next.
	var state, nonce, returnTo string
	err = s.st.DB.QueryRowContext(ctx,
		`DELETE FROM login_attempts WHERE id_hash = $1 AND expires_at > $2 RETURNING state, nonce, return_to`,
		accounts.HashToken(ck.Value), s.now()).Scan(&state, &nonce, &returnTo)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "sign-in expired; start again", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	got := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 || got == "" {
		http.Error(w, "sign-in state mismatch", http.StatusBadRequest)
		return
	}
	idn, err := s.idp.Exchange(ctx, r.URL.Query().Get("code"), nonce)
	if err != nil || idn.Sub == "" {
		http.Error(w, "sign-in failed", http.StatusBadRequest)
		return
	}

	u, notice, isNew, err := s.resolveUser(ctx, idn)
	if err != nil {
		if errors.Is(err, errAccountGone) {
			http.Error(w, "this account was deleted", http.StatusForbidden)
			return
		}
		s.cfg.Logger.Error("resolve user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A new session ID at every sign-in; any old one for this browser dies.
	if old, err := r.Cookie(sessionCookie); err == nil {
		_ = s.sessions.Delete(ctx, old.Value)
	}
	sid, _, err := s.sessions.Create(ctx, u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	dest := safeReturnTo(returnTo)
	if isNew {
		dest = "/settings"
	}
	if notice != "" {
		s.render(w, r, http.StatusOK, "notice", map[string]any{"Message": notice, "Next": dest})
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

var errAccountGone = errors.New("account past restore window")

func (s *server) resolveUser(ctx context.Context, idn Identity) (u *cortex.User, notice string, isNew bool, err error) {
	u, err = s.root.UserByGoogleSub(ctx, idn.Sub)
	switch {
	case err == nil:
		if u.DeletedAt != nil {
			if s.now().Sub(*u.DeletedAt) > restoreWindow {
				return nil, "", false, errAccountGone
			}
			if err := s.st.RestoreAccount(ctx, u.ID); err != nil {
				return nil, "", false, err
			}
			notice = "Your account was restored. Reconnect your assistants."
		}
		return u, notice, false, nil
	case !errors.Is(err, cortex.ErrNotFound):
		return nil, "", false, err
	}
	if idn.EmailVerified && s.cfg.BootstrapEmail != "" && strings.EqualFold(idn.Email, s.cfg.BootstrapEmail) {
		if bu, berr := s.root.BindBootstrap(ctx, idn.Sub, idn.Email, idn.Name); berr == nil {
			return bu, "", false, nil
		}
	}
	nu, err := s.root.CreateUser(ctx, idn.Sub, idn.Email, idn.Name, idn.EmailVerified)
	if err != nil {
		return nil, "", false, fmt.Errorf("create user: %w", err)
	}
	return nu, "", true, nil
}
