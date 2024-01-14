package brewfather

import "time"

type WebhookConfig struct {
	Name           string        `mapstructure:"name"`
	Url            string        `mapstructure:"url"`
	UpdateInterval time.Duration `mapstructure:"update_interval"`
}

type Config struct {
	UserId         string          `mapstructure:"user_id"`
	ApiKey         string          `mapstructure:"api_key"`
	UpdateInterval time.Duration   `mapstructure:"update_interval"`
	Webhooks       []WebhookConfig `mapstructure:"webhooks"`
}
