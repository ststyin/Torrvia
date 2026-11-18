// Torrvia is an AI-oriented media search service with Web, REST, and MCP interfaces backed by one local configuration file.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ststyin/torrvia/internal/adapter"
	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/download"
	"github.com/ststyin/torrvia/internal/mcpserver"
	"github.com/ststyin/torrvia/internal/qb"
	"github.com/ststyin/torrvia/internal/search"
	"github.com/ststyin/torrvia/internal/server"
	"github.com/ststyin/torrvia/internal/store"
)

//go:embed web/index.html
var webFS embed.FS

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showToken := flag.Bool("token", false, "Print the access token and exit")
	healthcheck := flag.Bool("healthcheck", false, "Run the container health check and exit")
	check := flag.Bool("check", false, "Check sites and downloader configuration, then exit")
	configPath := flag.String("config", "", "Configuration path (default ./config.toml; override with TORRVIA_CONFIG)")
	flag.Parse()

	path := *configPath
	if path == "" {
		path = config.DefaultPath()
	}

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if *showToken {
		fmt.Println(cfg.Server.Token)
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck(cfg))
	}
	if *check {
		os.Exit(runCheck(cfg))
	}

	// Publish the configured downloader and its save path together during a settings update.
	type runtimeConfig struct {
		cfg    *config.Config
		client *qb.Client
	}
	var current atomic.Pointer[runtimeConfig]
	current.Store(&runtimeConfig{cfg, newQBClient(cfg.Downloader)})

	adapters := buildAdapters(cfg)
	if len(adapters) == 0 {
		log.Printf("Warning: no sites available; enable at least one site in %s", cfg.Path())
	}
	svc := search.New(adapters, cfg.CacheTTL())

	hist, err := store.NewHistory(cfg.Server.DataDir)
	if err != nil {
		log.Printf("Search history unavailable; search remains enabled: %v", err)
		hist = nil
	}

	// Validate and build first, persist next, then publish the new configuration.
	apply := func(next *config.Config) error {
		// Validate before building so specific configuration errors are preserved.
		if err := next.Validate(); err != nil {
			return err
		}
		previous := current.Load()
		sitesChanged := !slices.Equal(next.Sites, previous.cfg.Sites) || next.CacheTTL() != previous.cfg.CacheTTL()
		var nextAdapters []adapter.Adapter
		if sitesChanged {
			nextAdapters = buildAdapters(next)
			if len(nextAdapters) != len(next.EnabledSites()) {
				return errors.New("Failed to initialize configured sites")
			}
		}
		if err := next.Save(path); err != nil {
			return err
		}
		if sitesChanged {
			svc.ReplaceAdapters(nextAdapters, next.CacheTTL())
		}
		client := previous.client
		if old, d := previous.cfg.Downloader, next.Downloader; old.URL != d.URL || old.Username != d.Username || old.Password != d.Password || old.Enabled() != d.Enabled() {
			client = newQBClient(d)
		}
		current.Store(&runtimeConfig{next, client})
		log.Printf("Configuration updated: %d sites, downloader enabled: %v", len(next.EnabledSites()), next.Downloader.Enabled())
		return nil
	}

	downloads := download.New(svc, func() (*qb.Client, string) {
		snapshot := current.Load()
		return snapshot.client, snapshot.cfg.Downloader.SavePath
	})
	mcpHandler := mcpserver.Handler(mcpserver.Deps{
		Downloads: downloads,
		Svc:       svc,
	})

	server.SetVersion(version)
	srv := server.New(server.Deps{
		Downloads: downloads,
		Cfg:       func() *config.Config { return current.Load().cfg },
		Svc:       svc,
		Hist:      hist,
		Web:       webFS,
		MCP:       mcpHandler,
		Apply:     apply,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Streaming searches use long-lived connections; leave the write timeout disabled.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	banner(cfg, adapters, cfg.Downloader.Enabled())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("Failed to start service: %v", err)
	case <-ctx.Done():
		log.Println("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
}

// buildAdapters builds enabled sites, skipping individual failures.
func buildAdapters(c *config.Config) []adapter.Adapter {
	sites := c.EnabledSites()
	out := make([]adapter.Adapter, 0, len(sites))
	for _, site := range sites {
		a, err := adapter.Build(site)
		if err != nil {
			log.Printf("Skipping site %s: %v", site.ID, err)
			continue
		}
		out = append(out, a)
	}
	return out
}

func newQBClient(d config.DownloaderConfig) *qb.Client {
	if !d.Enabled() {
		return nil
	}
	return qb.New(d.URL, d.Username, d.Password)
}

func banner(cfg *config.Config, adapters []adapter.Adapter, qbReady bool) {
	port := cfg.Server.Addr
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i:]
	}
	if port == ":0" {
		port = ""
	}

	ips := lanIPs()
	fmt.Println()
	fmt.Println("  Torrvia is running · Find what you want to watch.")
	fmt.Printf("  Version     %s\n", version)
	fmt.Printf("  Config      %s\n", cfg.Path())
	fmt.Printf("  Sites       %d available\n", len(adapters))
	for _, a := range adapters {
		fmt.Printf("            - %s (%s)\n", a.Name(), a.Type())
	}
	if qbReady {
		fmt.Printf("  Downloader  %s\n", cfg.Downloader.URL)
	} else {
		fmt.Println("  Downloader  Not configured (search only)")
	}
	fmt.Println()
	fmt.Printf("  Web         http://localhost%s\n", port)
	for _, ip := range ips {
		fmt.Printf("            http://%s%s\n", ip, port)
	}
	fmt.Println()
	fmt.Printf("  MCP         http://localhost%s/mcp\n", port)
	for _, ip := range ips {
		fmt.Printf("            http://%s%s/mcp\n", ip, port)
	}
	fmt.Println()
	fmt.Printf("  Token       %s\n", cfg.Server.Token)
	fmt.Println("            Request header: Authorization: Bearer <token>")
	fmt.Println()
	fmt.Println("  Claude Code setup:")
	fmt.Printf("    claude mcp add --transport http torrvia http://<local-ip>%s/mcp \\\n", port)
	fmt.Printf("      --header \"Authorization: Bearer %s\"\n", cfg.Server.Token)
	fmt.Println()
	fmt.Println("  Codex setup (~/.codex/config.toml):")
	fmt.Println("    [mcp_servers.torrvia]")
	fmt.Printf("    url = \"http://<local-ip>%s/mcp\"\n", port)
	fmt.Printf("    http_headers = { Authorization = \"Bearer %s\" }\n", cfg.Server.Token)
	fmt.Println()
}

// runHealthcheck supports container orchestration without curl or wget in the scratch image.
func runHealthcheck(cfg *config.Config) int {
	addr := cfg.Server.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	} else if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/health", nil)
	if err != nil {
		return 1
	}
	if cfg.Server.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// lanIPs returns local IPv4 addresses for access from other devices.
func lanIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}
