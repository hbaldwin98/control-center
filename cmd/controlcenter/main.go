// Command controlcenter runs the personal control center: one process, one box, one
// administrator, hosting compiled-in plugins behind a scoped host facade.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hbaldwin98/control-center/internal/config"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/notifications"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
	"github.com/hbaldwin98/control-center/internal/core/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "config/config.yaml", "path to the configuration file")
		staticDir  = flag.String("static", "web/dist", "directory holding the built frontend")
		logLevel   = flag.String("log-level", "info", "debug, info, warn, or error")
	)
	flag.Parse()

	level, err := parseLevel(*logLevel)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	masterKey, err := credentials.ParseMasterKey(os.Getenv("CC_MASTER_KEY"))
	if err != nil {
		return fmt.Errorf("CC_MASTER_KEY must be 64 hex characters (32-byte AES-256 key): %w", err)
	}

	// Signals cancel the root context; every subsystem shuts down from there.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, storage.Options{
		Path:        cfg.Data.DB,
		BusyTimeout: cfg.Data.BusyTimeout,
		ReadPool:    cfg.Data.ReadPool,
	})
	if err != nil {
		return err
	}
	defer store.Close()
	slog.Info("storage ready", "db", store.Path())

	blobs, err := storage.NewBlobStore(store, storage.BlobOptions{
		Dir:            cfg.Data.BlobDir,
		MaxObjectBytes: cfg.Blobs.MaxObjectBytes,
		MaxScopeBytes:  cfg.Blobs.MaxScopeBytes,
	})
	if err != nil {
		return err
	}
	// A crash can leave staged temp files and unreferenced objects. Collect them before
	// serving, so a restart exposes either the old blob or the new one, never a partial.
	if removed, err := blobs.Recover(ctx); err != nil {
		return err
	} else if removed > 0 {
		slog.Info("blob recovery removed incomplete files", "count", removed)
	}

	bus, err := events.New(store, store, events.Options{})
	if err != nil {
		return err
	}
	bus.Start(ctx)
	defer bus.Stop()
	go bus.RunRetentionDaily(ctx)

	creds, err := credentials.New(store, store, bus, credentials.Options{
		Keys:                map[int][]byte{1: masterKey},
		Active:              1,
		AllowedRedirectURIs: oauthRedirects(cfg),
		OAuth:               oauthProvidersFromEnv(),
	})
	if err != nil {
		return err
	}

	pol, err := policy.New(store, store, bus, nil)
	if err != nil {
		return err
	}
	// A crash can leave spend reservations whose outcome is unknowable. Count them at
	// the most they could have cost rather than silently undercounting.
	if n, err := pol.SettleOrphanedReservations(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Info("settled orphaned spend reservations at their reserved maximum", "count", n)
	}

	jq, err := jobs.New(store, store, bus, pol, jobs.Options{})
	if err != nil {
		return err
	}
	jq.Start(ctx)
	defer jq.Stop()

	engine, err := newBrowserEngine(cfg)
	if err != nil {
		return err
	}
	br, err := browser.New(bus, pol, browser.Options{Engine: engine})
	if err != nil {
		if c, ok := engine.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		return err
	}
	defer br.Close()

	// Providers and routes are administrator-owned state in SQLite, edited from
	// Settings against a live model catalog. The file only seeds an empty database.
	seed, err := ai.LoadSeed(cfg.AI.Models)
	if err != nil {
		return err
	}
	aisvc, err := ai.New(store, store, bus, pol, creds, ai.Options{
		Seed: seed,
		Refs: creds,
	})
	if err != nil {
		return err
	}

	notes, err := notifications.New(ctx, store, store, bus, creds, creds, notifications.Options{})
	if err != nil {
		return err
	}
	notes.Start(ctx)
	defer notes.Stop()

	if _, err := os.Stat(*staticDir); err != nil {
		slog.Warn("no frontend build found; serving placeholder", "dir", *staticDir)
		*staticDir = ""
	}

	ph, err := pluginhost.New(ctx, store, pluginhost.Options{
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: jq, AI: aisvc, Browser: br,
		Creds: creds, Refs: creds,
	})
	if err != nil {
		return err
	}
	if err := registerPlugins(ph); err != nil {
		return err
	}
	if err := ph.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = ph.Stop(context.Background()) }()

	srv, err := web.New(ctx, store, web.Deps{
		DB:            store,
		Config:        cfg,
		Events:        bus,
		Blobs:         blobs,
		Policy:        pol,
		Jobs:          jq,
		Credentials:   creds,
		AI:            aisvc,
		Notifications: notes,
		PluginHost:    ph,
		Plugins: func(ctx context.Context) []web.PluginDescriptor {
			ps := ph.ShellPlugins(ctx)
			out := make([]web.PluginDescriptor, 0, len(ps))
			for _, p := range ps {
				out = append(out, web.PluginDescriptor{ID: p.ID, Name: p.Name, Enabled: p.Enabled})
			}
			return out
		},
		StaticDir:         *staticDir,
		BootstrapPassword: os.Getenv("CC_BOOTSTRAP_PASSWORD"),
	})
	if err != nil {
		return err
	}

	return srv.ListenAndServe(ctx)
}

func newBrowserEngine(cfg config.Config) (browser.Engine, error) {
	switch strings.ToLower(cfg.Browser.Engine) {
	case "playwright":
		slog.Info("browser engine playwright")
		return browser.NewPlaywright()
	default:
		slog.Info("browser engine fake")
		routes := map[string]http.Handler{
			"hello.test":  browser.HTMLHandler(`<!doctype html><html><body><article class="lot">hello</article></body></html>`),
			"example.com": browser.HTMLHandler(`<!doctype html><html><body><main><h1>Example Domain</h1><p>This domain is for use in illustrative examples in documents.</p></main></body></html>`),
		}
		for name, handler := range fakePluginHosts() {
			routes[name] = handler
		}
		return browser.NewFake(routes), nil
	}
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return l, fmt.Errorf("invalid log level %q", s)
	}
	return l, nil
}

func oauthRedirects(cfg config.Config) []string {
	scheme := "http"
	if cfg.TLSEnabled() {
		scheme = "https"
	}
	const path = "/api/admin/credentials/oauth/callback"
	var out []string
	add := func(hostport string) {
		out = append(out, scheme+"://"+hostport+path)
	}
	host, port, err := net.SplitHostPort(cfg.Server.Addr)
	if err == nil {
		add(net.JoinHostPort(host, port))
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			add("localhost:" + port)
			add("127.0.0.1:" + port)
			add("[::1]:" + port)
		}
	}
	for _, o := range cfg.Server.Origins {
		out = append(out, strings.TrimRight(o, "/")+path)
	}
	return out
}

func oauthProvidersFromEnv() map[string]credentials.OAuthProvider {
	out := map[string]credentials.OAuthProvider{"codex": codexOAuthProvider()}
	if id := os.Getenv("CC_OAUTH_GOOGLE_CLIENT_ID"); id != "" {
		out["google"] = credentials.OAuthProvider{
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			ClientID:     id,
			ClientSecret: os.Getenv("CC_OAUTH_GOOGLE_CLIENT_SECRET"),
			Scopes:       []string{"openid", "email"},
		}
	}
	return out
}

// codexClientID is the public OAuth client the Codex CLI uses. It is a public client:
// it has no secret, it is pinned to one redirect, and PKCE is what protects the flow.
// Overridable so a future client id does not require a rebuild.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRedirectURI is registered against that client and cannot be changed by us. It
// points at the machine running the browser, not at this server, which is why the codex
// flow completes by pasting the callback URL back in.
const codexRedirectURI = "http://localhost:1455/auth/callback"

// codexOAuthProvider authorizes against a ChatGPT subscription rather than a platform
// API key. It is always configured: it needs no per-deployment secret, and a
// credential only exists once the administrator completes or imports a login.
func codexOAuthProvider() credentials.OAuthProvider {
	clientID := os.Getenv("CC_OAUTH_CODEX_CLIENT_ID")
	if clientID == "" {
		clientID = codexClientID
	}
	return credentials.OAuthProvider{
		AuthURL:     "https://auth.openai.com/oauth/authorize",
		TokenURL:    "https://auth.openai.com/oauth/token",
		ClientID:    clientID,
		RedirectURI: codexRedirectURI,
		Scopes:      []string{"openid", "profile", "email", "offline_access"},
		ExtraAuthParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
			"originator":                 "codex_cli_rs",
		},
		// The authorization-code exchange takes a form body; the refresh grant takes
		// JSON. That asymmetry is the provider's, not ours.
		RefreshJSON:  true,
		AccountClaim: []string{"https://api.openai.com/auth", "chatgpt_account_id"},
		PlanClaim:    []string{"https://api.openai.com/auth", "chatgpt_plan_type"},
	}
}
