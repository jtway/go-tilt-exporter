package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/brewtracker"
	"github.com/jtway/go-tilt-exporter/pkg/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bt, err := brewtracker.NewBrewTracker()
	if err != nil {
		log.Fatalf("brew tracker: %s", err)
	}

	shutdownTracing, err := telemetry.Setup(ctx, bt.Config.Tracing)
	if err != nil {
		log.Fatalf("tracing: %s", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(bt.Config.Prom.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("metrics server: %s", err)
		}
	}()

	runErr := bt.Run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := shutdownTracing(shutdownCtx); err != nil {
		log.Printf("tracing shutdown: %s", err)
	}
	if runErr != nil {
		log.Fatalf("brew tracker: %s", runErr)
	}
}
