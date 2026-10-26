package main

import (
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
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/render"
	"github.com/nmcginn/vitals/internal/sampler"
	"github.com/nmcginn/vitals/internal/server"
)

// Set at build time with -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

var levels = map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}

// run serves until ctx is done and returns the exit status: 2 for a bad flag,
// 1 for a failure after startup.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vitals", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:9110", "bind `address`; anything but loopback exposes the dashboard, which has no authentication")
	interval := fs.Duration("interval", 2*time.Second, "sample interval, at least 1s")
	history := fs.Duration("history", 15*time.Minute, "how much history to hold in memory; at least one -interval")
	// Empty means the built-in list.
	diskExclude := fs.String("disk-exclude", "", "`regex` of mount points or fs types to skip; replaces the built-in list")
	netExclude := fs.String("net-exclude", "", "`regex` of interface names to skip; replaces the built-in list")
	logLevel := fs.String("log-level", "info", "log `level`: debug, info, warn, or error")
	showVersion := fs.Bool("version", false, "print version, commit, and build date, then exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "vitals %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}

	level, ok := levels[*logLevel]
	var bad error
	switch {
	case fs.NArg() > 0:
		bad = fmt.Errorf("unexpected argument %q: every setting is a flag (see -h)", fs.Arg(0))
	case *interval < time.Second:
		bad = fmt.Errorf("-interval %v is too short: the minimum is 1s", *interval)
	case *history < *interval:
		bad = fmt.Errorf("-history %v is shorter than -interval %v: it must hold at least one sample", *history, *interval)
	case !ok:
		bad = fmt.Errorf("-log-level %q is not one of debug, info, warn, error", *logLevel)
	}
	for _, f := range []struct{ name, re string }{{"disk-exclude", *diskExclude}, {"net-exclude", *netExclude}} {
		if _, err := regexp.Compile(f.re); err != nil && bad == nil {
			bad = fmt.Errorf("-%s %q is not a valid regular expression: %v", f.name, f.re, strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		}
	}
	if bad != nil {
		fmt.Fprintln(stderr, "vitals:", bad)
		return 2
	}

	var opts collector.Options // validated above
	if *diskExclude != "" {
		opts.DiskExclude = regexp.MustCompile(*diskExclude)
	}
	if *netExclude != "" {
		opts.NetExclude = regexp.MustCompile(*netExclude)
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "vitals: cannot listen on -listen %s: %v\n", *listen, err)
		return 1
	}
	log.Info("listening", "addr", ln.Addr().String(), "version", version, "interval", *interval, "history", *history)
	if exposed(ln.Addr()) {
		log.Warn("listening on a non-loopback address: the dashboard is reachable from the network and has no authentication", "addr", ln.Addr().String())
	}

	store := sampler.NewStore(sampler.Capacity(*history, *interval))
	s := sampler.New(collector.New(opts), store, *interval, log)
	sctx, stopSampler := context.WithCancel(context.Background())
	sampled := make(chan struct{})
	go func() { s.Run(sctx); close(sampled) }()

	srv := &http.Server{
		Handler:           server.New(store, server.Options{Log: log, Build: render.Build{Version: version, Commit: commit}}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	status := 0
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-served:
		log.Error("serving failed", "err", err)
		status = 1
	}
	stopSampler()
	<-sampled
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("requests were still running after 5s; closed them", "err", err)
		status = 1
	}
	return status
}

// exposed reports whether the bound address is reachable from beyond this
// machine. 0.0.0.0 and [::] are: they listen on every interface.
func exposed(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	return !ok || !tcp.IP.IsLoopback()
}
