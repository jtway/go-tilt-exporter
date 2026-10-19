package brewfather

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type BrewTrackerWebhook struct {
	config     *WebhookConfig
	client     *http.Client
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

func NewBrewTrackerWebhook(config *WebhookConfig, client *http.Client) *BrewTrackerWebhook {
	webhook := &BrewTrackerWebhook{
		config: config,
		client: client,
	}
	return webhook
}

// Update posts to the Brewfather custom stream, at most once per
// update_interval. Returns sent=false (and no error) when rate-limited.
func (bt *BrewTrackerWebhook) Update(ctx context.Context, update *BrewTrackerStatus) (sent bool, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "brewfather.webhook.update",
		trace.WithAttributes(attribute.String("brewfather.webhook", bt.config.Name)))
	defer func() {
		span.SetAttributes(attribute.Bool("brewfather.webhook.sent", sent))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	return bt.update(ctx, update)
}

func (bt *BrewTrackerWebhook) update(ctx context.Context, update *BrewTrackerStatus) (bool, error) {
	// func (bt *BrewTrackerWebhook) Update(beer string, gravity float64, temp float32) error {
	nextUpdate := bt.lastUpdate.Add(bt.config.UpdateInterval)
	if nextUpdate.After(time.Now()) {
		// Silently return, no error, just not time.
		return false, nil
	}

	// update := &BrewTrackerStatus{
	// 	Name:        bt.config.Name,
	// 	Temperature: temp,
	// 	TempUnit:    "F",
	// 	Gravity:     gravity,
	// 	GravityUnit: "G",
	// }

	updateOut, err := json.Marshal(update)
	if err != nil {
		return false, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, bt.config.Url, bytes.NewReader(updateOut))
	if err != nil {
		return false, err
	}
	request.Header.Add("Content-Type", "application/json")

	response, err := bt.client.Do(request)
	if err != nil {
		return false, err
	}
	// TODO(jtway) look to see if there is a built in rate limiter for HTTP
	bt.lastUpdate = time.Now()

	defer response.Body.Close()
	// read body
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return true, err
	}

	var webhookResponse BrewTrackerStatusResponse
	err = json.Unmarshal(responseBody, &webhookResponse)
	if err != nil {
		return true, err
	}

	if webhookResponse.Result != "success" {
		return true, fmt.Errorf("error from webhook: %s", webhookResponse.Result)
	}
	// It all went well!
	return true, nil
}
