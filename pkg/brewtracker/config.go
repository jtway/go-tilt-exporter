package brewtracker

import (
	"fmt"
	"strings"
	"time"

	"github.com/jtway/go-tilt-exporter/pkg/batchsync"
	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/jtway/go-tilt-exporter/pkg/source"
	"github.com/jtway/go-tilt-exporter/pkg/telemetry"
	"github.com/spf13/viper"
)

type ConfigPrometheus struct {
	Port int `mapstructure:"port"`
}

// ConfigSource selects where Tilt readings come from.
type ConfigSource struct {
	// Type is "mqtt" (readings from remote receivers) or "ble" (scan locally).
	Type string `mapstructure:"type"`
	// CycleInterval is how often readings are processed; in BLE mode also
	// the scan duration.
	CycleInterval time.Duration `mapstructure:"cycle_interval"`
	// MaxReadingAge drops readings (and device series) older than this.
	MaxReadingAge time.Duration `mapstructure:"max_reading_age"`
	// Receiver labels BLE-mode readings (MQTT readings carry their own).
	Receiver string `mapstructure:"receiver"`
}

// ConfigComponents turns the components in this binary on or off. Each one
// only talks to the others through the broker, so any of them can later run
// as its own service by enabling just that one.
type ConfigComponents struct {
	BatchSync     bool `mapstructure:"batch_sync"`
	Brewtracker   bool `mapstructure:"brewtracker"`
	DeviceMetrics bool `mapstructure:"device_metrics"`
}

type Config struct {
	Brewfather brewfather.Config `mapstructure:"brewfather"`
	Prom       ConfigPrometheus  `mapstructure:"prom"`
	Source     ConfigSource      `mapstructure:"source"`
	MQTT       source.MQTTConfig `mapstructure:"mqtt"`
	Tracing    telemetry.Config  `mapstructure:"tracing"`
	BatchSync  batchsync.Config  `mapstructure:"batch_sync"`
	Components ConfigComponents  `mapstructure:"components"`
}

// ReadInConfig loads config.yaml and applies TILT_EXPORTER_* env overrides,
// e.g. TILT_EXPORTER_MQTT_PASSWORD, TILT_EXPORTER_BATCH_SYNC_MQTT_PASSWORD,
// TILT_EXPORTER_BREWFATHER_API_KEY.
func ReadInConfig() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/tilt-exporter/")
	viper.AddConfigPath("$HOME/.tilt-exporter")
	viper.AddConfigPath(".")

	// Defaults also register keys, which env overrides need with Unmarshal.
	for k, v := range map[string]any{
		"brewfather.api_base_url":       "",
		"brewfather.user_id":            "",
		"brewfather.api_key":            "",
		"brewfather.update_interval":    15 * time.Minute,
		"prom.port":                     9100,
		"source.type":                   "mqtt",
		"source.cycle_interval":         15 * time.Second,
		"source.max_reading_age":        5 * time.Minute,
		"source.receiver":               "local-ble",
		"mqtt.broker":                   "",
		"mqtt.username":                 "brewtracker",
		"mqtt.password":                 "",
		"mqtt.topic":                    "brewery/tilt/v1/+",
		"mqtt.topic_root":               "brewery",
		"mqtt.client_id":                "",
		"tracing.enabled":               false,
		"tracing.endpoint":              "",
		"tracing.service_name":          "go-tilt-exporter",
		"tracing.sample_ratio":          1.0,
		"batch_sync.refresh_interval":   5 * time.Minute,
		"batch_sync.statuses":           []string{"Fermenting"},
		"batch_sync.track_conditioning": false,
		"batch_sync.mqtt_username":      "batch-sync",
		"batch_sync.mqtt_password":      "",
		"components.batch_sync":         true,
		"components.brewtracker":        true,
		"components.device_metrics":     true,
	} {
		viper.SetDefault(k, v)
	}

	viper.SetEnvPrefix("TILT_EXPORTER")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("fatal error config file: %w", err)
	}
	config := &Config{}
	if err := viper.Unmarshal(config); err != nil {
		return nil, fmt.Errorf("unable to decode into config struct, %w", err)
	}
	return config, config.Validate()
}

// Validate checks cross-field requirements.
func (c *Config) Validate() error {
	if c.MQTT.Broker == "" {
		return ErrNoBroker
	}
	if c.Components.BatchSync && (c.Brewfather.UserId == "" || c.Brewfather.ApiKey == "") {
		return fmt.Errorf("batch_sync needs brewfather.user_id and brewfather.api_key")
	}
	switch c.Source.Type {
	case "mqtt", "ble":
	default:
		return fmt.Errorf("source.type must be mqtt or ble, got %q", c.Source.Type)
	}
	if c.Source.CycleInterval <= 0 {
		c.Source.CycleInterval = 15 * time.Second
	}
	if c.Prom.Port == 0 {
		c.Prom.Port = 9100
	}
	if !c.Components.BatchSync && !c.Components.Brewtracker && !c.Components.DeviceMetrics {
		return fmt.Errorf("all components are disabled")
	}
	return nil
}
