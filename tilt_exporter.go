package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/batchsync"
	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/jtway/go-tilt-exporter/pkg/brewtracker"
	"github.com/jtway/go-tilt-exporter/pkg/contract"
	"github.com/jtway/go-tilt-exporter/pkg/devicemetrics"
	"github.com/jtway/go-tilt-exporter/pkg/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := zap.NewExample().Sugar()
	defer logger.Sync()

	cfg, err := brewtracker.ReadInConfig()
	logger.Infof("Read Config File: %s", viper.ConfigFileUsed())
	if err != nil {
		log.Fatalf("config: %s", err)
	}

	shutdownTracing, err := telemetry.Setup(ctx, cfg.Tracing)
	if err != nil {
		log.Fatalf("tracing: %s", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Prom.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("metrics server: %s", err)
		}
	}()

	// Components only meet through the broker; each can be disabled here and
	// run elsewhere.
	topics := contract.Topics{Root: cfg.MQTT.TopicRoot}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	run := func(name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Infof("starting component %s", name)
			if err := f(); err != nil {
				errs <- errors.New(name + ": " + err.Error())
				stop()
			}
		}()
	}

	var dm *devicemetrics.Exporter
	if cfg.Components.DeviceMetrics {
		dm = devicemetrics.New(cfg.Source.MaxReadingAge, logger)
		if cfg.Source.Type == "mqtt" {
			run(devicemetrics.Component, func() error { return dm.RunMQTT(ctx, cfg.MQTT) })
		} else {
			// BLE: no broker feed; brewtracker hands readings over in-process.
			run(devicemetrics.Component, func() error { dm.RunSweeper(ctx); return nil })
		}
	}
	if cfg.Components.BatchSync {
		api := brewfather.NewBrewfatherClient(&cfg.Brewfather, logger)
		run(batchsync.Component, func() error {
			return batchsync.Run(ctx, cfg.BatchSync, cfg.MQTT.Broker, api, topics, logger)
		})
	}
	if cfg.Components.Brewtracker {
		bt := brewtracker.NewBrewTracker(cfg, logger)
		if dm != nil && cfg.Source.Type == "ble" {
			bt.OnReading = dm.Observe
		}
		run(brewtracker.Component, func() error { return bt.Run(ctx) })
	}

	<-ctx.Done()
	wg.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := shutdownTracing(shutdownCtx); err != nil {
		log.Printf("tracing shutdown: %s", err)
	}
	close(errs)
	failed := false
	for err := range errs {
		log.Printf("component failed: %s", err)
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}
