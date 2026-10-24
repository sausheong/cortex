// Package web serves the cortex web UI: Google sign-in, sessions, the AI
// settings page and the mount points later pages and the OAuth server use.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/web/pages"
)

//go:embed static
var staticFS embed.FS

// Options plugs later tasks into the server.
type Options struct {
	// Enqueue schedules a background job ("reembed", "index", ...). Nil makes
	// a settings change that needs a re-embed fail with a clear error.
	Enqueue func(ctx context.Context, userID, kind string) (jobID string, err error)
	// OnKeyed, when set, is called on every request that carries a user key
	// (an unlocked session). It must clone uk if it keeps it: the request's
	// copy is zeroed when the handler returns.
	OnKeyed func(userID string, uk *keys.UK)
	// OnLogout, when set, is called after a session is ended by logout so the
	// caller can drop any key it holds for the user.
	OnLogout func(userID string)
	// MCP, when set, is served at /mcp.
	MCP http.Handler
	// Mounts register further routes; they use only Deps.
	Mounts []func(mux *http.ServeMux, d Deps)
	// Now is the clock (nil = time.Now); it is also used for sessions.
	Now func() time.Time
}

// Config is the server configuration.
type Config struct {
	PublicURL, BootstrapEmail, GoogleClientID, GoogleClientSecret string
	AllowPrivate                                                  bool
	Identity                                                      IdentityProvider // nil = Google from the ID/secret
	Logger                                                        *slog.Logger
	// EncryptionEnroll enables passkey enrollment (CORTEX_ENCRYPTION_ENROLL).
	// It stays false in production until the owner migration is complete.
	EncryptionEnroll bool
}

// Deps is what mounted handlers receive.
type Deps struct {
	Root     *cortex.Cortex
	Accounts *accounts.Store
	Sessions *Sessions
	Cfg      Config
	Now      func() time.Time
	// Auth requires a live session and checks CSRF on POST.
	Auth func(next func(w http.ResponseWriter, r *http.Request, u SessionUser)) http.HandlerFunc
	// Render writes a page inside the layout with security headers.
	Render func(w http.ResponseWriter, r *http.Request, name string, data any)
	// RenderStatus is Render with an explicit HTTP status.
	RenderStatus func(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

type server struct {
	cfg      Config
	opts     Options
	root     *cortex.Cortex
	st       *accounts.Store
	sessions *Sessions
	idp      IdentityProvider
	tmpl     map[string]*pages.Template
	now      func() time.Time

	authLimit *accounts.Limiter
	testLimit *accounts.Limiter
	zipLimit  *accounts.Limiter

	// Unlock and enrollment throttles (see passkey.go).
	unlockUser   *accounts.Limiter
	unlockIP     *accounts.Limiter
	recoveryUser *accounts.Limiter
	vcache       vaultCache

	// Seams for tests.
	testProvider func(ctx context.Context, s accounts.Settings) error
	indexExists  func(ctx context.Context, dims int) (bool, error)
}

func newServer(cfg Config, root *cortex.Cortex, st *accounts.Store, opts Options) *server {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &server{cfg: cfg, opts: opts, root: root, st: st, now: now,
		sessions:  NewSessions(st, now),
		tmpl:      pages.Load(),
		authLimit: accounts.NewLimiter(30, 30, now),
		testLimit: accounts.NewLimiter(10, 10, now),

		unlockUser:   accounts.NewLimiter(10, 10, now),
		unlockIP:     accounts.NewLimiter(30, 30, now),
		recoveryUser: accounts.NewLimiter(1, 5, now),
	}
	s.configurePasskeys()
	s.idp = cfg.Identity
	if s.idp == nil {
		s.idp = newGoogleIdentity(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.PublicURL+"/auth/google/callback")
	}
	s.testProvider = func(ctx context.Context, set accounts.Settings) error {
		return accounts.TestProvider(ctx, set, accounts.SafeClient(st.AllowPrivate, accounts.ChatBodyCap))
	}
	s.indexExists = s.pgIndexExists
	return s
}

// NewServer builds the web handler.
func NewServer(cfg Config, root *cortex.Cortex, st *accounts.Store, opts Options) http.Handler {
	return newServer(cfg, root, st, opts).handler()
}

func (s *server) deps() Deps {
	return Deps{Root: s.root, Accounts: s.st, Sessions: s.sessions, Cfg: s.cfg, Now: s.now,
		Auth: s.auth,
		Render: func(w http.ResponseWriter, r *http.Request, name string, data any) {
			s.render(w, r, http.StatusOK, name, data)
		},
		RenderStatus: s.render}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /auth/google/start", s.googleStart)
	mux.HandleFunc("GET /auth/google/callback", s.googleCallback)
	mux.HandleFunc("POST /logout", s.auth(s.logout))
	mux.HandleFunc("GET /settings", s.authUnlocked(s.settingsGet))
	mux.HandleFunc("POST /settings", s.authUnlocked(s.settingsPost))
	mux.HandleFunc("POST /settings/reembed", s.authUnlocked(s.settingsReembed))
	mux.HandleFunc("GET /settings/job/{id}", s.authUnlocked(s.jobProgress))
	s.mountPasskeys(mux)
	s.mountMemories(mux)
	s.mountGraph(mux)
	s.mountVault(mux)
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	if s.opts.MCP != nil {
		mux.Handle("/mcp", s.opts.MCP)
	}
	d := s.deps()
	for _, m := range s.opts.Mounts {
		m(mux, d)
	}
	return securityHeaders(mux)
}

type nonceKey struct{}

// cspHeader is the page policy. Scripts run only from this origin or with the
// per-response nonce; there are no inline styles or event-handler attributes.
// The consent page omits form-action: its POST redirects to the client's own
// redirect URI, which browsers would otherwise block.
func cspHeader(nonce string, formAction bool) string {
	p := "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'"
	if formAction {
		p += "; form-action 'self' https://accounts.google.com"
	}
	return p
}

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := newNonce()
		h := w.Header()
		h.Set("Content-Security-Policy", cspHeader(n, true))
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, n)))
	})
}

// sessionFor resolves the session without touching the key wrap: never
// Unlocked, no Key.
func (s *server) sessionFor(r *http.Request) (SessionUser, bool) {
	return s.lookup(r, false)
}

func (s *server) lookup(r *http.Request, withKey bool) (SessionUser, bool) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return SessionUser{}, false
	}
	secret := ""
	if withKey {
		if uc, err := r.Cookie(unlockCookie); err == nil {
			secret = uc.Value
		}
	}
	u, err := s.sessions.Lookup(r.Context(), ck.Value, secret)
	return u, err == nil
}

// unlockCookieFor builds the cortex_unlock cookie: HttpOnly, Secure, Lax, and
// never longer-lived than the session.
func unlockCookieFor(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: unlockCookie, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// keyed tells the hook that this request carries a key.
func (s *server) keyed(u SessionUser) {
	if s.opts.OnKeyed != nil && u.Key != nil {
		s.opts.OnKeyed(u.UserID, u.Key)
	}
}

// auth requires a live session; POSTs also need the session's CSRF token.
func (s *server) auth(next func(w http.ResponseWriter, r *http.Request, u SessionUser)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.lookup(r, true)
		if ok {
			defer u.Key.Zero()
			s.keyed(u)
		}
		if !ok {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/auth/google/start?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			http.Error(w, "sign in required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			tok := r.Header.Get("X-CSRF-Token")
			if tok == "" {
				tok = r.PostFormValue("csrf")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(u.CSRF)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r, u)
	}
}

// render writes name inside the layout. data is wrapped in pages.Page with
// the viewer's CSRF token.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	t, ok := s.tmpl[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := pages.Page{Data: data, Path: r.URL.Path}
	if n, ok := r.Context().Value(nonceKey{}).(string); ok {
		p.Nonce = n
		w.Header().Set("Content-Security-Policy", cspHeader(n, name != "consent"))
	}
	if u, ok := s.sessionFor(r); ok {
		p.CSRF, p.SignedIn = u.CSRF, true
		// Every signed-in page needs it: the nav shrinks while setup is open.
		p.Setup, p.SetUp = s.setupRead(r.Context(), u.UserID)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		s.cfg.Logger.Error("render", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		_ = s.sessions.Delete(r.Context(), ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, unlockCookieFor("", -1))
	if s.opts.OnLogout != nil {
		s.opts.OnLogout(u.UserID)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) pgIndexExists(ctx context.Context, dims int) (bool, error) {
	var ok bool
	err := s.st.DB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = ANY(current_schemas(false))
		 AND indexname = 'emb_hnsw_memory_' || $1)`, strconv.Itoa(dims)).Scan(&ok)
	return ok, err
}
