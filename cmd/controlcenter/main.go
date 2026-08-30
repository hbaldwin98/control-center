// Command controlcenter runs the personal control center: one process, one box, one
// administrator, hosting compiled-in plugins behind a scoped host facade.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hbaldwin98/control-center/internal/config"
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

	if _, err := os.Stat(*staticDir); err != nil {
		slog.Warn("no frontend build found; serving placeholder", "dir", *staticDir)
		*staticDir = ""
	}

	srv, err := web.New(ctx, store, web.Deps{
		DB:        store,
		Config:    cfg,
		StaticDir: *staticDir,
	})
	if err != nil {
		return err
	}

	if err := registerPlugins(ctx); err != nil {
		return err
	}

	return srv.ListenAndServe(ctx)
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return l, fmt.Errorf("invalid log level %q", s)
	}
	return l, nil
}
