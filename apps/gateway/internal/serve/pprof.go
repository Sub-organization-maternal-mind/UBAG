package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// startPprof serves net/http/pprof on UBAG_PPROF_ADDR. Unset/blank means no
// listener (inert default). The host must be a loopback IP literal or
// "localhost"; anything else (including an empty host such as ":6060", which
// would bind every interface) is refused so profiling never becomes a remote
// attack surface. The returned stop func is always safe to call.
func startPprof(raw string) (func(), error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return func() {}, nil
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return func() {}, fmt.Errorf("UBAG_PPROF_ADDR must be host:port: %w", err)
	}
	bindHost := host
	if strings.EqualFold(host, "localhost") {
		bindHost = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return func() {}, errors.New("UBAG_PPROF_ADDR must use a loopback host (127.0.0.1, ::1 or localhost)")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bindHost, port))
	if err != nil {
		return func() {}, fmt.Errorf("UBAG_PPROF_ADDR listen: %w", err)
	}

	// Own mux: importing net/http/pprof also registers on DefaultServeMux, but
	// the gateway never serves that, so the listener above is the only route.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// No WriteTimeout: /profile and /trace legitimately stream for ?seconds=N.
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	slog.Info("starting ubag gateway pprof (loopback only)", "addr", listener.Addr().String())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if srv.Shutdown(ctx) != nil {
			_ = srv.Close()
		}
	}, nil
}
