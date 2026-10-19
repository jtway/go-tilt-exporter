package brewtracker

import (
	"fmt"
	"strings"
	"time"

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
	// Type is "ble" (scan locally, the original mode) or "mqtt" (subscribe to
	// readings published by remote receivers).
	Type string `mapstructure:"type"`
	// CycleInterval is how often readings are processed. In BLE mode it's
	// also the scan duration.
	CycleInterval time.Duration `mapstructure:"cycle_interval"`
	// MaxReadingAge drops readings older than this (MQTT mode), e.g. a
	// retained message from a receiver that has since died.
	MaxReadingAge time.Duration `mapstructure:"max_reading_age"`
	// Receiver labels BLE-mode readings (MQTT readings carry their own).
	Receiver string `mapstructure:"receiver"`
}

type Config struct {
	Brewfather brewfather.Config `mapstructure:"brewfather"`
	Prom       ConfigPrometheus  `mapstructure:"prom"`
	Source     ConfigSource      `mapstructure:"source"`
	MQTT       source.MQTTConfig `mapstructure:"mqtt"`
	Tracing    telemetry.Config  `mapstructure:"tracing"`
}

// ReadInConfig loads config.yaml and applies TILT_EXPORTER_* env overrides,
// e.g. TILT_EXPORTER_MQTT_PASSWORD, TILT_EXPORTER_BREWFATHER_API_KEY.
func ReadInConfig() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/tilt-exporter/")
	viper.AddConfigPath("$HOME/.tilt-exporter")
	viper.AddConfigPath(".")

	// Defaults also register the keys, which env overrides need to work
	// with Unmarshal.
	viper.SetDefault("brewfather.api_base_url", "")
	viper.SetDefault("brewfather.user_id", "")
	viper.SetDefault("brewfather.api_key", "")
	viper.SetDefault("brewfather.update_interval", 15*time.Minute)
	viper.SetDefault("prom.port", 9100)
	viper.SetDefault("source.type", "ble")
	viper.SetDefault("source.cycle_interval", 15*time.Second)
	viper.SetDefault("source.max_reading_age", 5*time.Minute)
	viper.SetDefault("source.receiver", "local-ble")
	viper.SetDefault("mqtt.broker", "")
	viper.SetDefault("mqtt.username", "")
	viper.SetDefault("mqtt.password", "")
	viper.SetDefault("mqtt.topic", "brewery/tilt/v1/+")
	viper.SetDefault("mqtt.client_id", "")
	viper.SetDefault("tracing.enabled", false)
	viper.SetDefault("tracing.endpoint", "")
	viper.SetDefault("tracing.service_name", "go-tilt-exporter")
	viper.SetDefault("tracing.sample_ratio", 1.0)

	viper.SetEnvPrefix("TILT_EXPORTER")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

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
		return nil, fmt.Errorf("Both user id and api key are required config values.")
	}
	switch config.Source.Type {
	case "ble":
	case "mqtt":
		if config.MQTT.Broker == "" {
			return nil, fmt.Errorf("source.type=mqtt requires mqtt.broker")
		}
	default:
		return nil, fmt.Errorf("source.type must be ble or mqtt, got %q", config.Source.Type)
	}
	if config.Source.CycleInterval <= 0 {
		config.Source.CycleInterval = 15 * time.Second
	}
	if config.Prom.Port == 0 {
		config.Prom.Port = 9100
	}
	return config, nil
}
