// Command prometheus-noderings-sd discovers NodeRings virtual machines for
// Prometheus. It serves them over HTTP service discovery (http_sd_configs)
// and can also write them to a file for file_sd_configs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	promslogflag "github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/noderings/prometheus-noderings-sd/internal/discovery"
	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

const name = "prometheus-noderings-sd"

func main() {
	app := kingpin.New(name, "Prometheus service discovery for NodeRings virtual machines.")
	var (
		apiURL = app.Flag("noderings.api-url", "NodeRings API URL.").
			Envar("NODERINGS_API_URL").Default(noderings.DefaultEndpoint).String()
		tokenFile = app.Flag("noderings.token-file", "File holding the NodeRings API token. The NODERINGS_TOKEN environment variable is used when this is not set.").
				Envar("NODERINGS_TOKEN_FILE").PlaceHolder("FILE").String()
		orgID = app.Flag("noderings.organization-id", "Organization to discover VMs in. Defaults to the token's organization.").
			Envar("NODERINGS_ORGANIZATION_ID").PlaceHolder("ID").String()
		refresh = app.Flag("discovery.refresh-interval", "How often to fetch the VM list from the NodeRings API.").
			Default("60s").Duration()
		port = app.Flag("target.port", "Port added to each target address. HTTP SD clients can override it with ?port=.").
			Default("80").Int()
		family = app.Flag("target.address-family", "Address used for targets: ipv4 or ipv6. A VM without that address falls back to the other.").
			Default(discovery.AddressIPv4).Enum(discovery.AddressIPv4, discovery.AddressIPv6)
		tagSep = app.Flag("target.tag-separator", "Separator for list labels such as __meta_noderings_tags.").
			Default(",").String()
		targetsPath = app.Flag("web.targets-path", "Path under which to serve the HTTP SD target list.").
				Default("/targets").String()
		metricsPath = app.Flag("web.telemetry-path", "Path under which to expose the service's own metrics.").
				Default("/metrics").String()
		outputFile = app.Flag("file.output", "Also write the targets to this file for file_sd_configs. Empty disables it.").
				PlaceHolder("FILE").String()
		webConfig = kingpinflag.AddFlags(app, ":10064")
	)
	logConfig := &promslog.Config{}
	promslogflag.AddFlags(app, logConfig)
	app.Version(version.Print(name))
	app.HelpFlag.Short('h')
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logger := promslog.New(logConfig)
	if err := run(logger, options{
		apiURL: *apiURL, tokenFile: *tokenFile, orgID: *orgID, refresh: *refresh,
		port: *port, family: *family, tagSep: *tagSep, targetsPath: *targetsPath,
		metricsPath: *metricsPath, outputFile: *outputFile, web: webConfig,
	}); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}

type options struct {
	apiURL, tokenFile, orgID string
	refresh                  time.Duration
	port                     int
	family, tagSep           string
	targetsPath, metricsPath string
	outputFile               string
	web                      *web.FlagConfig
}

func run(logger *slog.Logger, o options) error {
	if o.port < 1 || o.port > 65535 {
		return fmt.Errorf("--target.port must be from 1 to 65535, got %d", o.port)
	}
	if o.refresh < 10*time.Second {
		return fmt.Errorf("--discovery.refresh-interval must be at least 10s, got %s", o.refresh)
	}
	token, err := loadToken(o.tokenFile)
	if err != nil {
		return err
	}
	client, err := noderings.New(noderings.Config{
		Endpoint:       o.apiURL,
		Token:          token,
		OrganizationID: o.orgID,
		UserAgent:      name + "/" + version.Version,
		Logger:         logger,
	})
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		versioncollector.NewCollector("noderings_sd"),
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	opts := discovery.Options{Port: o.port, AddressFamily: o.family, TagSeparator: o.tagSep}
	d := discovery.New(client, opts, logger, reg)

	if o.outputFile != "" {
		fw := &discovery.FileWriter{Path: o.outputFile}
		d.OnUpdate(func(vms []noderings.VirtualMachine) {
			if err := fw.Write(discovery.Build(vms, opts)); err != nil {
				logger.Error("writing the file_sd output failed", "err", err)
			}
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Fail fast on a token the API rejects; other errors are retried in the loop.
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = d.Refresh(check)
	cancel()
	if err != nil {
		if noderings.IsUnauthenticated(err) {
			return fmt.Errorf("the NodeRings API rejected the token: %w", err)
		}
		logger.Warn("first refresh failed; retrying", "err", err)
	}
	go func() {
		// Run refreshes immediately; skip that when the check above succeeded.
		if d.Ready() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.refresh):
			}
		}
		d.Run(ctx, o.refresh)
	}()

	mux := http.NewServeMux()
	mux.Handle(o.targetsPath, d.Handler())
	mux.Handle(o.metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK\n")) })
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !d.Ready() {
			http.Error(w, "the NodeRings VM list has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("OK\n"))
	})
	if o.metricsPath != "/" && o.targetsPath != "/" {
		landing, err := web.NewLandingPage(web.LandingConfig{
			Name:        name,
			Description: "Prometheus service discovery for NodeRings virtual machines",
			Version:     version.Info(),
			Links: []web.LandingLinks{
				{Address: o.targetsPath, Text: "Targets", Description: "HTTP SD target list (add ?port=9100 to change the port)"},
				{Address: o.metricsPath, Text: "Metrics", Description: "Metrics about the service discovery itself"},
				{Address: "/-/healthy", Text: "Health"},
			},
		})
		if err != nil {
			return err
		}
		mux.Handle("/", landing)
	}

	logger.Info("starting "+name, "version", version.Info(), "build_context", version.BuildContext(),
		"vms", d.VMCount(), "refresh_interval", o.refresh, "target_port", o.port, "file_output", o.outputFile)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := web.ListenAndServe(srv, o.web, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loadToken(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading --noderings.token-file: %w", err)
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
		return "", fmt.Errorf("--noderings.token-file %s is empty", file)
	}
	if t := strings.TrimSpace(os.Getenv("NODERINGS_TOKEN")); t != "" {
		return t, nil
	}
	return "", errors.New("set NODERINGS_TOKEN or --noderings.token-file to a NodeRings API token")
}
