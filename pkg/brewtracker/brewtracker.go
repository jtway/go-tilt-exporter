package brewtracker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jtway/go-tilt"
	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type BrewTracker struct {
	config  *Config
	metrics *metrics
	logger  *zap.SugaredLogger

	brewfatherClient     *brewfather.BrewfatherClient
	brewFatherLastUpdate time.Time

	scannerRunDone       context.Context
	scannerRunDoneCancel context.CancelFunc
}

func NewBrewTracker() *BrewTracker {
	var bt BrewTracker

	bt.metrics = NewMetrics()
	bt.logger = zap.NewExample().Sugar()

	config, err := ReadInConfig()
	bt.logger.Infof("Read Config File: %s", viper.ConfigFileUsed())
	if err != nil {
		panic(fmt.Errorf("Failed to read and set config, %w", err))
	}
	if config == nil {
		panic(fmt.Errorf("Unexpected nil config."))
	}
	bt.config = config
	bt.brewfatherClient = brewfather.NewBrewfatherClient(&config.Brewfather)
	bt.scannerRunDone, bt.scannerRunDoneCancel = context.WithCancel(context.Background())

	return &bt
}

func (bt *BrewTracker) recordMetics() {
	// We're realistically going to want to do this periodically
	// Also, this all needs to be refactored to be way more efficient
	batches, err := bt.brewfatherClient.GetActiveBatches()
	bt.brewFatherLastUpdate = time.Now()
	if err != nil {
		panic(fmt.Errorf("Unable to retrieve batches, %w", err))
	}

	s := tilt.NewScanner()
	go func() {
		for {
			if bt.brewFatherLastUpdate.Add(bt.config.Brewfather.UpdateInterval).Before(time.Now()) {
				batches, err = bt.brewfatherClient.GetActiveBatches()
				bt.brewFatherLastUpdate = time.Now()
				if err != nil {
					panic(fmt.Errorf("Unable to retrieve batches, %w", err))
				}
			}
			s.Scan(20 * time.Second)
			fmt.Printf("Found %d tilts", len(s.Tilts()))
			for _, t := range s.Tilts() {
				name := "unknown"
				for _, batch := range batches {
					for _, tilt := range batch.Devices.Tilt.Items {
						color := string(t.Colour())
						if strings.EqualFold(tilt.Name, color) {
							name = batch.Name
							beerMeasuredOriginalGravity.WithLabelValues(batch.Id, name, color).Set(float64(batch.MeasuredOg))
							beerEstimatedFinalGravity.WithLabelValues(batch.Id, name, color).Set(float64(batch.EstimatedFg))
							beerEstimatedIbu.WithLabelValues(batch.Id, name, color).Set(float64(batch.EstimatedIbu))
							beerEstimatedSrm.WithLabelValues(batch.Id, name, color).Set(float64(batch.EstimatedColor))
						}
					}
				}
				beerReading.WithLabelValues(string(t.Colour())).Inc()
				beerGravity.WithLabelValues(string(t.Colour()), name).Set(t.Gravity())
				beerTemperatureF.WithLabelValues(string(t.Colour()), name).Set(float64(t.Fahrenheit()))
				beerTemperatureC.WithLabelValues(string(t.Colour()), name).Set(float64(t.Celsius()))
			}
			time.Sleep(10 * time.Second)
		}
	}()
}

// Run until canceled.
func (bt *BrewTracker) Run() error {

	// Setup the scanner, with it running until canceled.
	// Use a ticker to retrieve batches every so often
	// For each tilt received check current batches and update.
	// Then report metrics.
	// I could use another ticket for the webhook, or just use the internalized rate limit

	return nil
}
