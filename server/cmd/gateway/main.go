package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mioreel-gateway/server/internal/api"
	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/service"
	"mioreel-gateway/server/internal/upstream"
)

func main() {
	var (
		configPath string
		listenAddr string
		port       int
	)
	flag.StringVar(&configPath, "config", "", "path to optional JSON configuration file")
	flag.StringVar(&listenAddr, "listen", "", "override listen address (e.g. :18662 or 127.0.0.1:18662)")
	flag.IntVar(&port, "port", 0, "override listen port (1..65535)")
	flag.Parse()

	logger := observability.NewLogger(os.Stdout, slog.LevelInfo)

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	resolvedAddr, err := config.ResolveListenAddr(cfg.ListenAddr, listenAddr, port)
	if err != nil {
		logger.Error("invalid listen configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}
	cfg.ListenAddr = resolvedAddr

	if len(cfg.AuthTokens) == 0 && !cfg.AllowInsecureNoAuth {
		fmt.Fprintln(os.Stderr, "ERROR: authentication is required; set MIOREEL_AUTH_TOKENS (comma-separated) or MIOREEL_ALLOW_INSECURE_NO_AUTH=true for local development")
		os.Exit(2)
	}
	if len(cfg.AuthTokens) == 0 && cfg.AllowInsecureNoAuth {
		logger.Warn("running with MIOREEL_ALLOW_INSECURE_NO_AUTH=true; do not expose this instance publicly")
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metrics := observability.NewMetrics()
	profileMgr := profile.NewManager(cfg, logger, metrics)
	profileMgr.StartRefresher(rootCtx)

	httpClient := upstream.NewClient()
	hongguoCli := upstream.NewHongguoClient(httpClient, metrics)
	deviceMgr := device.NewManager(httpClient, cfg.DeviceStorePath, logger)

	seriesSvc := service.NewSeriesService(profileMgr, deviceMgr, hongguoCli)
	catalogSvc := service.NewCatalogService(profileMgr, deviceMgr, hongguoCli)
	discoverySvc := service.NewDiscoveryService(profileMgr, deviceMgr, hongguoCli)
	mediaSvc := service.NewMediaService(cfg.MediaProxyEnabled, cfg.MediaTicketSecret, cfg.MediaFFmpegPath, cfg.MediaCacheDir)
	resolveSvc := service.NewResolveService(profileMgr, deviceMgr, hongguoCli, seriesSvc, mediaSvc, logger, metrics, cfg.DefaultResolveTimeoutMs)
	searchSvc := service.NewSearchService(profileMgr, deviceMgr, hongguoCli)
	danmakuSvc := service.NewDanmakuService(profileMgr, deviceMgr, hongguoCli)

	handler := api.NewHandler(api.ServerDeps{
		Config:   cfg,
		Logger:   logger,
		Metrics:  metrics,
		Profiles: profileMgr,
		Devices:  deviceMgr,
		Upstream:  hongguoCli,
		Catalog:   catalogSvc,
		Discovery: discoverySvc,
		Series:    seriesSvc,
		Resolve:  resolveSvc,
		Search:   searchSvc,
		Danmaku:  danmakuSvc,
		Media:    mediaSvc,
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("mioreel-gateway listening",
			slog.String("addr", cfg.ListenAddr),
			slog.String("version", cfg.Version),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutting down gracefully", slog.String("signal", sig.String()))
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Error("graceful shutdown error", slog.String("error", err.Error()))
		}
	case err := <-errCh:
		if err != nil {
			logger.Error("http server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}
}
