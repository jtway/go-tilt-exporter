package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jtway/go-tilt"
	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type ConfigBrewfather struct {
	UserId      string `mapstructure:"user_id"`
	ApiKey      string `mapstructure:"api_key"`
	TiltWebhook string `mapstructure:"tilt_webhook"`
}

type ConfigPrometheus struct {
	Port uint16 `mapstructure:"port"`
}

type Config struct {
	Brewfather ConfigBrewfather `mapstructure:"brewfather"`
	Prom       ConfigPrometheus `mapstructure:"prom"`
}

// func yamlStringSettings() string {
// 	c := viper.AllSettings()
// 	bs, err := yaml.Marshal(c)
// 	if err != nil {
// 		log.Fatalf("unable to marshal config to YAML: %v", err)
// 	}
// 	return string(bs)
// }

func GetActiveBatches(config *Config) ([]brewfather.Batch, error) {
	brewClient := brewfather.NewBrewfatherClient(config.Brewfather.UserId, config.Brewfather.ApiKey)

	batches, err := brewClient.GetBatches()
	if err != nil {
		return nil, err
	}

	var activeBatches []brewfather.Batch
	for _, batchShort := range batches {
		fmt.Printf("Batch Name: %s\n", batchShort.Name)
		fmt.Printf("Status %s\n", batchShort.Status)
		if batchShort.Status == brewfather.Fermenting || batchShort.Status == brewfather.Conditioning {
			batchId := batchShort.Id
			batch, err := brewClient.GetBatch(batchId)
			if err != nil {
				return nil, err
			}
			activeBatches = append(activeBatches, *batch)
		}
	}
	return activeBatches, nil
}

func recordMetics(config *Config) {
	// We're realistically going to want to do this periodically
	// Also, this all needs to be refactored to be way more efficient
	batches, err := GetActiveBatches(config)
	if err != nil {
		panic(fmt.Errorf("Unable to retrieve batches, %w", err))
	}

	s := tilt.NewScanner()
	go func() {
		for {
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

var (
	beerReading = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tilt_readings_taken_total",
		Help: "total number of beer readings taken",
	},
		[]string{"colour"},
	)
)
var (
	beerMeasuredOriginalGravity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "brew_batch_measured_og",
		Help: "Brewfather measured original gravity",
	},
		[]string{"id", "name", "tilt_color"},
	)
)

var (
	beerEstimatedFinalGravity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "brew_batch_estimated_fg",
		Help: "Brewfather estimated final gravity",
	},
		[]string{"id", "name", "tilt_color"},
	)
)

var (
	beerEstimatedIbu = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "brew_batch_estimated_ibu",
		Help: "Brewfather estimated IBU",
	},
		[]string{"id", "name", "tilt_color"},
	)
)

var (
	beerEstimatedSrm = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "brew_batch_estimated_srm",
		Help: "Brewfather estimated SRM",
	},
		[]string{"id", "name", "tilt_color"},
	)
)

var (
	beerGravity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tilt_gravity_reading",
		Help: "latest specfic gravity reading",
	},
		[]string{"color", "name"},
	)
)

var (
	beerTemperatureF = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tilt_temperature_reading_f",
		Help: "latest temperature reading",
	},
		[]string{"color", "name"},
	)
)

var (
	beerTemperatureC = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tilt_temperature_reading_c",
		Help: "latest temperature reading",
	},
		[]string{"color", "name"},
	)
)

func readConfig() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/tilt-exporter/")
	viper.AddConfigPath("$HOME/.tilt-exporter")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		return nil, fmt.Errorf("fatal error config file: %w", err)
	}

	config := &Config{}
	err = viper.Unmarshal(config)
	if err != nil {
		return nil, fmt.Errorf("unable to decode into config struct, %w", err)
	}

	if len(config.Brewfather.UserId) == 0 || len(config.Brewfather.ApiKey) == 0 {
		return nil, fmt.Errorf("Both user id and api key are required config values. %v", config)
	}
	if config.Prom.Port == 0 {
		config.Prom.Port = 9100
	}
	return config, nil

}

func main() {
	logger := zap.NewExample().Sugar()

	defer logger.Sync()

	config, err := readConfig()
	logger.Infof("Read Config File: %s", viper.ConfigFileUsed())
	if err != nil {
		panic(fmt.Errorf("Failed to read and set config, %w", err))
	}
	if config == nil {
		panic(fmt.Errorf("Unexpected nil config."))
	}

	recordMetics(config)
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":9100", nil)
}
