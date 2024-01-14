package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/jtway/go-tilt-exporter/pkg/brewtracker"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	brewtracker := brewtracker.NewBrewTracker()

	err := brewtracker.Run()
	if err != nil {
		panic(fmt.Errorf("Failed running brew tracker. %w", err))
	}

	http.Handle("/metrics", promhttp.Handler())
	promAddress := ":" + strconv.Itoa(brewtracker.Config.Prom.Port)
	http.ListenAndServe(promAddress, nil)
}
