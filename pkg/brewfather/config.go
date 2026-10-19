package brewfather

import "time"

type WebhookConfig struct {
	Name           string        `mapstructure:"name"`
	Url            string        `mapstructure:"url"`
	UpdateInterval time.Duration `mapstructure:"update_interval"`
	Enabled        bool          `mapstructure:"enabled"`
}

type Config struct {
	// ApiBaseUrl defaults to the public v2 API; overridable for tests.
	ApiBaseUrl     string          `mapstructure:"api_base_url"`
	UserId         string          `mapstructure:"user_id"`
	ApiKey         string          `mapstructure:"api_key"`
	UpdateInterval time.Duration   `mapstructure:"update_interval"`
	Webhooks       []WebhookConfig `mapstructure:"webhooks"`
}
