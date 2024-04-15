package brewfather

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type BrewTrackerWebhook struct {
	config     *WebhookConfig
	lastUpdate time.Time
}

// This is a little different than the lib I'm using, so for now using this struct
type BrewTrackerStatus struct {
	Name        string  `json:"name"`
	BeerName    string  `json:"beer"`
	Gravity     float64 `json:"gravity"`
	GravityUnit string  `json:"gravity_unit"`
	Temperature float32 `json:"temp"`
	TempUnit    string  `json:"temp_unit"`
}

// The Brewfather webhook has a single key in the response, "result"
type BrewTrackerStatusResponse struct {
	Result string `json:"result"`
}

func NewBrewTrackerWebhook(config *WebhookConfig) *BrewTrackerWebhook {
	webhook := &BrewTrackerWebhook{
		config: config,
	}
	return webhook
}

func (bt *BrewTrackerWebhook) Update(beer string, gravity float64, temp float32) error {
	nextUpdate := bt.lastUpdate.Add(bt.config.UpdateInterval)
	if nextUpdate.After(time.Now()) {
		// Silently return, no error, just not time.
		return nil
	}

	update := &BrewTrackerStatus{
		Name:        bt.config.Name,
		Temperature: temp,
		TempUnit:    "F",
		Gravity:     gravity,
		GravityUnit: "G",
	}

	updateOut, err := json.Marshal(update)
	if err != nil {
		return err
	}

	client := &http.Client{}
	request, err := http.NewRequest(http.MethodPost, bt.config.Url, bytes.NewReader(updateOut))
	if err != nil {
		return err
	}
	request.Header.Add("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	// TODO(jtway) look to see if there is a built in rate limiter for HTTP
	bt.lastUpdate = time.Now()

	defer response.Body.Close()
	// read body
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	var webhookResponse BrewTrackerStatusResponse
	err = json.Unmarshal(responseBody, &webhookResponse)
	if err != nil {
		return err
	}

	if webhookResponse.Result != "success" {
		return fmt.Errorf("Error from webhook: %s", webhookResponse.Result)
	}
	// It all went well!
	return nil
}
