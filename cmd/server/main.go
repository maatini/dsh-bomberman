// Command server starts Bomber Grid: a server-authoritative multiplayer
// Bomberman-style game rendered with Go templates and HTMX.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bomberman/internal/game"
	"bomberman/internal/lobby"
	webapp "bomberman/internal/web"
	"bomberman/internal/ws"
)

var version = "dev"

func main() {
	addr := flag.String("addr", envOr("BM_ADDR", "127.0.0.1:4301"), "HTTP listen address")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("bomber-grid", version)
		return
	}

	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("BM_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	manager := lobby.NewManager(logger)
	defer manager.Close()

	// BM_MATCH_SECONDS shortens the stalling phase, which is handy for demos,
	// tests and short lunch-break rounds. The default stays at two minutes.
	if raw := strings.TrimSpace(os.Getenv("BM_MATCH_SECONDS")); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil || secs < 5 {
			logger.Warn("ignoring BM_MATCH_SECONDS", "value", raw)
		} else {
			cfg := game.DefaultConfig()
			cfg.MatchTicks = secs * game.TickRate
			manager.SetGameConfig(cfg)
			logger.Info("custom sudden death", "seconds", secs)
		}
	}

	webServer, err := webapp.New(manager, logger)
	if err != nil {
		logger.Error("initialize web server", "error", err)
		os.Exit(1)
	}
	hub := ws.NewHub(manager, webServer.RenderLive, logger)
	defer hub.Close()

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           webServer.Handler(hub),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Error("listen", "addr", *addr, "error", err)
		os.Exit(1)
	}
	logger.Info("bomber grid listening", "url", displayURL(*addr), "version", version)

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(ln)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
		}
	}

	hub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Warn("graceful shutdown timed out", "error", err)
		_ = httpServer.Close()
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func displayURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
