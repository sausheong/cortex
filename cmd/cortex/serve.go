package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
	mcpserver "github.com/sausheong/cortex/cmd/cortex/mcp"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/jobs"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/web"
	"github.com/sausheong/cortex/internal/web/oauth"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultPublicURL = "https://cortex.sausheong.dev"

// serveApp is a started cortex service: its handler and a Close that stops
// the job pool and releases the database connections.
type serveApp struct {
	Handler http.Handler
	Close   func()
}

// runServe builds the service in its fixed startup order: (1) require the
// secret key and database URL, (2) open the database (applies migrations),
// (3) seed the bootstrap user, (4) start the job pool. The caller listens
// only after it returns.
func runServe(ctx context.Context, env map[string]string) (*serveApp, error) {
	secret := env["CORTEX_SECRET_KEY"]
	if secret == "" {
		return nil, errors.New("cortex serve: CORTEX_SECRET_KEY is required (32 bytes, hex)")
	}
	sealer, err := accounts.NewSealer(secret)
	if err != nil {
		return nil, fmt.Errorf("cortex serve: CORTEX_SECRET_KEY: %w", err)
	}
	dsn := env["CORTEX_DATABASE_URL"]
	if dsn == "" {
		return nil, errors.New("cortex serve: set CORTEX_DATABASE_URL or pass --db <postgres-url>")
	}
	publicURL := strings.TrimRight(env["CORTEX_PUBLIC_URL"], "/")
	if publicURL == "" {
		publicURL = defaultPublicURL
	}
	bootEmail := env["CORTEX_BOOTSTRAP_EMAIL"]
	if bootEmail == "" {
		bootEmail = "sausheong@gmail.com"
	}

	root, err := cortex.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("cortex serve: %w", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("cortex serve: open database: %w", err)
	}
	st := &accounts.Store{DB: db, Sealer: sealer, AllowPrivate: env["CORTEX_ALLOW_PRIVATE_PROVIDERS"] == "1"}
	cleanup := func() { db.Close(); root.Close() }

	if err := accounts.MigrateBootstrap(ctx, st, env); err != nil {
		cleanup()
		return nil, fmt.Errorf("cortex serve: bootstrap: %w", err)
	}

	// keyAttachTTL is how long a keyed request lets the pool hold a key clone.
	const keyAttachTTL = 15 * time.Minute

	var pool *jobs.Pool
	pool = jobs.NewPool(2, func(ctx context.Context, j jobs.Job) error {
		if id, ok := strings.CutPrefix(j.Kind, jobs.CompactKindPrefix); ok {
			return jobs.CompactJobKey(ctx, root, st, j.UserID, id, j.Key)
		}
		switch j.Kind {
		case jobs.RememberKind:
			return jobs.RememberJobKey(ctx, root, st, j.UserID, j.Payload, j.Key)
		case "index":
			return jobs.IndexJobKey(ctx, root, st, j.UserID, j.Key)
		case accounts.MigrateEncKind:
			if j.Key == nil {
				return model.ErrLocked
			}
			return jobs.MigrateOwner(ctx, root, st, j.UserID, j.Key, func(done, total int) { pool.Progress(ctx, j.ID, done, total) })
		case "reembed":
			set, err := st.GetSettings(ctx, j.UserID, j.Key)
			if err != nil {
				return err
			}
			_, emb, _, err := accounts.Providers(set, set, accounts.SafeClient(st.AllowPrivate, accounts.ChatBodyCap))
			if err != nil {
				return err
			}
			cx := root.ForUser(j.UserID)
			if j.Key != nil {
				state, serr := st.EncState(ctx, j.UserID)
				if serr != nil {
					return serr
				}
				cx = cx.WithKey(j.Key, state)
				defer cx.Release()
			}
			return jobs.ReEmbedWith(ctx, cx, st, emb, set, func(done, total int) { pool.Progress(ctx, j.ID, done, total) })
		default:
			return fmt.Errorf("unknown job kind %q", j.Kind)
		}
	}).WithDB(db)
	pool.Start(ctx)

	// A keyed request (web or MCP) lets background work run for that user:
	// it attaches a clone of the key to the pool (resuming parked jobs) and
	// starts maintenance when it is stale. Both clone uk before returning, so
	// the request may zero its copy.
	onKeyed := func(userID string, uk *keys.UK) {
		if uk == nil {
			return
		}
		// A migrating owner gets a runnable migration job before the key
		// attaches, so the attach resumes it (also after a failed run).
		_ = jobs.EnsureMigrationJob(context.Background(), pool, userID)
		pool.AttachKey(userID, uk, keyAttachTTL)
		jobs.MaintainOnUse(root, st, uk, userID, jobs.NightlyOptions{Enqueue: pool.Enqueue})
	}

	resolve := func(ctx context.Context, bearer string) (*cortex.Cortex, error) {
		userID, connID, uk, err := st.ResolveBearer(ctx, bearer)
		if err != nil {
			return nil, err
		}
		defer uk.Zero() // the handle keeps its own clone
		onKeyed(userID, uk)
		return st.ResolveUser(ctx, root, userID, connID, uk)
	}
	s := server.NewMCPServer("cortex", "1.0.0", server.WithToolCapabilities(false))
	mcpserver.RegisterToolsFor(s, nil, publicURL, mcpserver.WithBackgroundRemember(
		func(ctx context.Context, cx *cortex.Cortex, content, source, speaker string) error {
			uk := cx.CloneKey()
			defer uk.Zero()
			_, err := jobs.EnqueueRemember(ctx, pool, cx.UserID(), jobs.RememberPayload{
				Content: content, Source: source, Speaker: speaker, ConnID: cx.ConnectionID()}, uk)
			return err
		}))
	mcpHandler := mcpserver.NewHTTPHandler(s, resolve, publicURL, accounts.NewLimiter(30, 30, nil))

	h := buildHandler(handlerDeps{
		Cfg: web.Config{
			PublicURL: publicURL, BootstrapEmail: bootEmail,
			GoogleClientID: env["GOOGLE_OAUTH_CLIENT_ID"], GoogleClientSecret: env["GOOGLE_OAUTH_CLIENT_SECRET"],
			AllowPrivate: st.AllowPrivate,
			// On by default; CORTEX_ENCRYPTION_ENROLL=0 turns enrollment off.
			EncryptionEnroll: envBoolDefault(env["CORTEX_ENCRYPTION_ENROLL"], true),
		},
		Root: root, Store: st, MCP: mcpHandler, Enqueue: pool.Enqueue, OnKeyed: onKeyed, OnLogout: pool.Detach,
	})

	return &serveApp{Handler: h, Close: func() { pool.Stop(); cleanup() }}, nil
}

// handlerDeps is everything buildHandler needs to assemble the full HTTP
// surface of `cortex serve`.
type handlerDeps struct {
	Cfg     web.Config
	Root    *cortex.Cortex
	Store   *accounts.Store
	MCP     http.Handler
	Enqueue func(ctx context.Context, userID, kind string) (string, error)
	// OnKeyed runs on every key-bearing web request (see web.Options.OnKeyed).
	OnKeyed func(userID string, uk *keys.UK)
	// OnLogout drops the pool's key for the user.
	OnLogout func(userID string)
	Now      func() time.Time
}

// buildHandler assembles every route served by `cortex serve`: the web UI,
// /mcp, the connections and compaction pages, and the OAuth server. It is the
// single place routes are wired so a test can exercise the real thing.
func buildHandler(d handlerDeps) http.Handler {
	return web.NewServer(d.Cfg, d.Root, d.Store, web.Options{
		MCP: d.MCP, Enqueue: d.Enqueue, OnKeyed: d.OnKeyed, OnLogout: d.OnLogout, Now: d.Now,
		Mounts: []func(*http.ServeMux, web.Deps){
			web.ConnectionsMount,
			web.CompactionMount(d.Enqueue),
			oauth.Mount(oauth.Options{}), // CIMD fetches use the hardened default client
		},
	})
}

func environMap() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func cmdServe() {
	fs := flag.NewFlagSet("cortex serve", flag.ExitOnError)
	addr := fs.String("addr", mcpserver.EnvOr("CORTEX_ADDR", "127.0.0.1:8080"), "listen address")
	_ = fs.Parse(os.Args[2:])

	env := environMap()
	if dbDSN != "" {
		env["CORTEX_DATABASE_URL"] = dbDSN
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := runServe(ctx, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer app.Close()

	srv := &http.Server{Addr: *addr, Handler: app.Handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	fmt.Fprintf(os.Stderr, "cortex serve: listening on %s\n", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// envBool reads a boolean environment value ("1", "true", "yes", "on").
func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// envBoolDefault is envBool with a default for an unset or unrecognised value.
func envBoolDefault(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
