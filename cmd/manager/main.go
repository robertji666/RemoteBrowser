package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/clipboard"
	"github.com/robertji666/RemoteBrowser/internal/config"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/files"
	"github.com/robertji666/RemoteBrowser/internal/httpapi"
	"github.com/robertji666/RemoteBrowser/internal/proxy"
	"github.com/robertji666/RemoteBrowser/internal/session"
	"github.com/robertji666/RemoteBrowser/internal/store"
	"github.com/robertji666/RemoteBrowser/internal/view"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(os.Args[1:], os.Stdin); err != nil {
		slog.Error("manager failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader) (resultErr error) {
	flags := flag.NewFlagSet("manager", flag.ContinueOnError)
	migrateOnly := flags.Bool("migrate-only", false, "Run database migrations and exit")
	resetAdmin := flags.Bool("reset-admin-password", false, "Reset admin password from RB_RESET_ADMIN_PASSWORD or stdin, then exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *migrateOnly && *resetAdmin {
		return errors.New("choose either -migrate-only or -reset-admin-password")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	openStore := store.NewLocked
	if *resetAdmin {
		openStore = store.New
	}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	closeDatabase := true
	defer func() {
		if closeDatabase {
			resultErr = errors.Join(resultErr, st.Close())
		}
	}()

	// Password reset is a bounded transaction against an existing schema and
	// must work while the manager holds its lifecycle lock. Schema migrations
	// remain exclusive and are never run by this administrative command.
	if !*resetAdmin {
		if err := st.MigrateWithDefaults(context.Background(), cfg.DefaultInstanceQuota); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
	}

	if *migrateOnly {
		slog.Info("migrations completed")
		return nil
	}

	authService := auth.New(cfg, st)
	if *resetAdmin {
		password := os.Getenv("RB_RESET_ADMIN_PASSWORD")
		if password == "" {
			line, readErr := bufio.NewReader(input).ReadString('\n')
			if readErr != nil && len(line) == 0 {
				return fmt.Errorf("read reset password: %w", readErr)
			}
			password = strings.TrimRight(line, "\r\n")
		}
		if err := authService.ResetAdminPassword(context.Background(), password); err != nil {
			return fmt.Errorf("reset admin password: %w", err)
		}
		slog.Info("admin password reset; old login credentials revoked")
		return nil
	}
	if err := authService.InitDefaultUser(context.Background()); err != nil {
		return fmt.Errorf("initialize administrator: %w", err)
	}

	dockerClient, err := docker.NewClient()
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer dockerClient.Close()

	sessionService := session.NewService(
		st,
		dockerClient,
		cfg.MaxSessions,
		cfg.SessionIdleTimeout,
		cfg.DataDir,
		cfg.SessionHostDataDir,
		cfg.SessionNanoCPUs,
		cfg.SessionMemoryBytes,
		cfg.SessionShmBytes,
		cfg.DockerNetworkName,
		cfg.WebRTCICEIP,
		cfg.ScreenWidth,
		cfg.ScreenHeight,
		cfg.ScreenDepth,
		cfg.ChromeWindowTop,
		cfg.ChromeWindowBottom,
		cfg.WebRTCWidth,
		cfg.WebRTCHeight,
		cfg.WebRTCFramerate,
		cfg.WebRTCVideoCodec,
		cfg.WebRTCVideoBitrate,
		cfg.WebRTCAudioBitrate,
		cfg.PublishSessionTCPPorts,
	)
	if err := sessionService.Init(context.Background()); err != nil {
		return fmt.Errorf("initialize instance service: %w", err)
	}

	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	maintenanceDone := make(chan struct{})
	closeDatabase = false
	go func() { defer close(maintenanceDone); sessionService.StartMaintenance(appCtx) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := shutdownBackground(ctx, cancelApp, sessionService.Shutdown, maintenanceDone); err != nil {
			// Do not close a database that workers may still be using. main will
			// exit with this error; OS process teardown releases the service lock.
			resultErr = errors.Join(resultErr, err)
			return
		}
		closeDatabase = true
	}()

	proxyService := proxy.New(sessionService, dockerClient)
	clipboardService := clipboard.NewService()
	filesService := files.NewService(st)
	if filesService == nil {
		return errors.New("file service initialization failed")
	}

	viewEngine, err := view.New()
	if err != nil {
		return fmt.Errorf("create view engine: %w", err)
	}

	handler := httpapi.NewHandler(authService, sessionService, proxyService, clipboardService, filesService, viewEngine, dockerClient, cfg.MaxUploadSize)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	handler.Register(r)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return appCtx },
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("manager starting", "addr", cfg.HTTPAddr)
		serverErr <- srv.ListenAndServe()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	select {
	case <-sig:
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
	}
	cancelApp()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	return nil
}

// shutdownBackground only succeeds after both lifecycle workers and the
// maintenance loop have stopped; callers can then safely close their database.
func shutdownBackground(ctx context.Context, cancel context.CancelFunc, shutdown func(context.Context) error, maintenanceDone <-chan struct{}) error {
	cancel()
	if err := shutdown(ctx); err != nil {
		return fmt.Errorf("stop instance workers: %w", err)
	}
	select {
	case <-maintenanceDone:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for instance maintenance: %w", ctx.Err())
	}
}
