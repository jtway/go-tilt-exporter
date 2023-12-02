package brewtracker

import (
	"fmt"

	"github.com/jtway/go-tilt-exporter/pkg/brewfather"
	"github.com/spf13/viper"
)

type ConfigPrometheus struct {
	Port uint16 `mapstructure:"port"`
}

type Config struct {
	Brewfather brewfather.Config `mapstructure:"brewfather"`
	Prom       ConfigPrometheus  `mapstructure:"prom"`
}

func ReadInConfig() (*Config, error) {
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
