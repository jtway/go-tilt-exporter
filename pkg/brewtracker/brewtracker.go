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
	Config  *Config
	metrics *metrics
	Logger  *zap.SugaredLogger

	BrewfatherClient     *brewfather.BrewfatherClient
	brewFatherLastUpdate time.Time

	scannerRunDone       context.Context
	scannerRunDoneCancel context.CancelFunc
}

func NewBrewTracker() *BrewTracker {
	var bt BrewTracker

	bt.metrics = NewMetrics()
	bt.Logger = zap.NewExample().Sugar()

	config, err := ReadInConfig()
	bt.Logger.Infof("Read Config File: %s", viper.ConfigFileUsed())
	if err != nil {
		panic(fmt.Errorf("Failed to read and set config, %w", err))
	}
	if config == nil {
		panic(fmt.Errorf("Unexpected nil config."))
	}
	bt.Config = config
	bt.BrewfatherClient = brewfather.NewBrewfatherClient(&config.Brewfather, bt.Logger)
	bt.scannerRunDone, bt.scannerRunDoneCancel = context.WithCancel(context.Background())

	return &bt
}

// Setup the scanner, with it running until canceled.
// Use a ticker to retrieve batches every so often
// For each tilt received check current batches and update.
// Then report metrics.
// I could use another ticket for the webhook, or just use the internalized rate limit

// Run until canceled.
func (bt *BrewTracker) Run() error {
	defer bt.Logger.Sync()
	// We're realistically going to want to do this periodically
	// Also, this all needs to be refactored to be way more efficient
	bt.Logger.Infof("Fetching initial batches")
	batches, err := bt.BrewfatherClient.GetActiveBatches()
	bt.brewFatherLastUpdate = time.Now()
	if err != nil {
		return fmt.Errorf("Unable to retrieve batches, %w", err)
	}
	bt.Logger.Infof("Working with %d active batches", len(batches))

	s := tilt.NewScanner()
	go func() {
		for {
			if bt.brewFatherLastUpdate.Add(bt.Config.Brewfather.UpdateInterval).Before(time.Now()) {
				bt.Logger.Infof("Fetching updated active batches.")
				updatedBatches, err := bt.BrewfatherClient.GetActiveBatches()
				bt.brewFatherLastUpdate = time.Now()
				if err != nil {
					bt.Logger.Errorf("Unable to retrieve batches, %s", err.Error())
				}
				// Only if we got a valid response swap them out.
				if len(updatedBatches) > 0 {
					batches = updatedBatches
				}
				bt.Logger.Infof("Refreshed batches with %d active batches.", len(batches))
			}
			// Eventually it would be nice for the bluetooth scanning, and other telemetry to
			// be another go routine. That way on the update interval we would just grab the
			// latest readings.
			s.Scan(20 * time.Second)
			bt.Logger.Infof("Scanning found %d tilts", len(s.Tilts()))
			for _, t := range s.Tilts() {
				name := "unknown"
				for _, batch := range batches {
					for _, tilt := range batch.GetTilts() {
						color := string(t.Colour())
						// Increment counter for readings for the tilt
						bt.metrics.beerReading.WithLabelValues(color).Inc()

						if strings.EqualFold(tilt.Name, color) {
							// If we have a matching tilt, update using our custom stream
							bt.Logger.Infof("Update batch telemtry via webhook")
							err := batch.UpdateWebhook(t.Gravity(), float32(t.Fahrenheit()))
							if err != nil {
								bt.Logger.Errorf("Unable to update via webhook: %s", err.Error())
							}
							name = batch.Name

							bt.metrics.beerMeasuredOriginalGravity.WithLabelValues(batch.Id, name).Set(float64(batch.MeasuredOg))
							bt.metrics.beerEstimatedFinalGravity.WithLabelValues(batch.Id, name).Set(float64(batch.EstimatedFg))
							bt.metrics.beerEstimatedIbu.WithLabelValues(batch.Id, name).Set(float64(batch.EstimatedIbu))
							bt.metrics.beerEstimatedSrm.WithLabelValues(batch.Id, name).Set(float64(batch.EstimatedColor))
							bt.metrics.beerGravity.WithLabelValues(batch.Id, name, color).Set(t.Gravity())
							bt.metrics.beerTemperatureF.WithLabelValues(batch.Id, name, color).Set(float64(t.Fahrenheit()))
							bt.metrics.beerTemperatureC.WithLabelValues(color, name, color).Set(float64(t.Celsius()))
						}
					}
				}

			}
			time.Sleep(10 * time.Second)
		}
	}()

	return nil
}
