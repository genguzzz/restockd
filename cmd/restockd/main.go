// Command restockd 自托管 VPS 补货监控与自动锁单服务：单二进制，内嵌前端，SQLite 存储。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/genguzzz/restockd/internal/api"
	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/engine"
	_ "github.com/genguzzz/restockd/internal/provider/blossom"
	_ "github.com/genguzzz/restockd/internal/provider/usvps24"
	_ "github.com/genguzzz/restockd/internal/provider/whmcs"
	"github.com/genguzzz/restockd/internal/store"
	"github.com/genguzzz/restockd/web"
)

func main() {
	listen := flag.String("listen", envOr("RESTOCKD_LISTEN", "127.0.0.1:8080"), "HTTP 监听地址")
	dataDir := flag.String("data", envOr("RESTOCKD_DATA", "./data"), "数据目录（SQLite）")
	publicURL := flag.String("public-url", os.Getenv("RESTOCKD_PUBLIC_URL"), "对外访问地址，用于通知中的链接")
	debug := flag.Bool("debug", os.Getenv("RESTOCKD_DEBUG") == "1", "调试日志")
	seed := flag.Bool("seed", true, "数据库为空时写入默认站点（novixlink cox、usvps24）")
	flag.Parse()
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(logger, *listen, *dataDir, *publicURL, *seed); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func run(logger *slog.Logger, listen, dataDir, publicURL string, seed bool) error {
	password := os.Getenv("RESTOCKD_PASSWORD")
	if password == "" {
		return errors.New("RESTOCKD_PASSWORD is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(filepath.Join(dataDir, "restockd.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	eng, err := engine.New(engine.Options{Store: st, Log: logger, PublicURL: publicURL})
	if err != nil {
		return err
	}
	if seed {
		if err := seedSites(ctx, st); err != nil {
			return err
		}
	}
	if err := eng.Start(ctx); err != nil {
		return err
	}
	defer eng.Stop()
	static, err := web.Dist()
	if err != nil {
		return err
	}
	handler, err := api.New(ctx, api.Options{Engine: eng, Store: st, Log: logger, Password: password, Static: static})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", listen)
		errCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// seedSites 首次启动预置两个站点；之后由界面管理。
func seedSites(ctx context.Context, st *store.Store) error {
	sites, err := st.ListSites(ctx)
	if err != nil || len(sites) > 0 {
		return err
	}
	defaults := []domain.Site{
		{ID: "novixlink", Kind: "whmcs", Name: "NovixLink 诺联", BaseURL: "https://novixlink.com", Config: map[string]string{"groups": "cox-isp-vps", "currency": "CAD", "location": "US Las Vegas (Cox ISP)"}, IntervalSec: 60, FetchMode: domain.FetchFlareSolverr, Enabled: true},
		{ID: "usvps24", Kind: "usvps24", Name: "usvps24", BaseURL: "https://usvps24.com", Config: map[string]string{}, IntervalSec: 60, FetchMode: domain.FetchDirect, Enabled: true},
	}
	for _, site := range defaults {
		if err := st.SaveSite(ctx, site); err != nil {
			return err
		}
	}
	return nil
}
