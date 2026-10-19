package brewfather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

const tracerName = "github.com/jtway/go-tilt-exporter/pkg/brewfather"

const (
	api_base_url = "https://api.brewfather.app/v2/"
)

type BrewfatherClient struct {
	client *http.Client
	logger *zap.SugaredLogger
	config *Config

	webhooks map[string]*BrewTrackerWebhook
}

func NewBrewfatherClient(config *Config, logger *zap.SugaredLogger) *BrewfatherClient {

	brewClient := &BrewfatherClient{
		config:   config,
		logger:   logger,
		webhooks: make(map[string]*BrewTrackerWebhook),
	}
	// otelhttp gives every Brewfather call a client span with W3C propagation.
	brewClient.client = &http.Client{
		Timeout:   time.Second * 10,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	for i := range config.Webhooks {
		webhookConfig := &config.Webhooks[i]
		fmt.Printf("Creating webhook %s, enabled: %t\n", webhookConfig.Name, webhookConfig.Enabled)
		brewClient.webhooks[webhookConfig.Name] = NewBrewTrackerWebhook(webhookConfig, brewClient.client)
	}
	return brewClient
}

func (b *BrewfatherClient) GetBatches(ctx context.Context) ([]BatchShort, error) {
	var batches []BatchShort
	url := b.baseURL() + "batches"

	request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(b.config.UserId, b.config.ApiKey)

	for {
		response, err := b.client.Do(request)
		if err != nil {
			return batches, err
		}
		body, err := io.ReadAll(response.Body) // response body is []byte
		response.Body.Close()
		if err != nil {
			return batches, err
		}
		if response.StatusCode != http.StatusOK {
			return batches, fmt.Errorf("brewfather GET batches: %s", response.Status)
		}

		var batchesPage []BatchShort
		if err := json.Unmarshal(body, &batchesPage); err != nil { // Parse []byte to the go struct pointer
			return batches, fmt.Errorf("Can not unmarshal JSON")
		}

		batchesReturned := len(batchesPage)
		b.logger.Infof("returned %d batches", batchesReturned)
		for _, batch := range batchesPage {
			b.logger.Infof("batch: %s, batch: id: %s, status: %s", batch.Name, batch.Id, batch.Status)
		}
		if batchesReturned == 0 {
			break
		}

		batches = append(batches, batchesPage...)

		time.Sleep(1)
		queryValues := request.URL.Query()
		queryValues.Set("start_after", batchesPage[batchesReturned-1].Id)
		request.URL.RawQuery = queryValues.Encode()
		b.logger.Infof("attempting to get more batches. Starting after: %s", batchesPage[batchesReturned-1].Id)
		b.logger.Infof("URL: %v", request.URL)
	}
	return batches, nil
}

func (b *BrewfatherClient) GetBatch(ctx context.Context, batchId string) (*Batch, error) {
	url := b.baseURL() + "batches/" + batchId

	request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(b.config.UserId, b.config.ApiKey)
	response, err := b.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body) // response body is []byte
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brewfather GET batch %s: %s", batchId, response.Status)
	}

	var batch Batch
	if err := json.Unmarshal(body, &batch); err != nil { // Parse []byte to the go struct pointer
		return nil, fmt.Errorf("Can not unmarshal JSON")
	}
	tilts := batch.GetTilts()
	if len(tilts) != 0 {
		for _, t := range tilts {
			fmt.Printf("Found tilt %s, enabled %t (batch-id: %s)\n", t.Key, t.Enabled, t.BatchId)
		}
	}
	streams := batch.GetStreams()
	if len(streams) != 0 {
		for _, stream := range streams {
			for name, webhook := range b.webhooks {
				if strings.EqualFold(stream.Name, name) {
					fmt.Printf("Attached webhook %s to %s\n", name, batch.Name)
					batch.BrewTracker = webhook

				}
			}
		}
	}
	return &batch, nil
}

func (b *BrewfatherClient) baseURL() string {
	if b.config.ApiBaseUrl != "" {
		return strings.TrimSuffix(b.config.ApiBaseUrl, "/") + "/"
	}
	return api_base_url
}

// GetActiveBatches returns fermenting batches with their devices resolved.
func (b *BrewfatherClient) GetActiveBatches(ctx context.Context) (_ []Batch, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "brewfather.get_active_batches")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	batches, err := b.GetBatches(ctx)
	if len(batches) == 0 && err != nil {
		return nil, err
	}
	if err != nil {
		b.logger.Infof("Batches were returned, but so was an error: %s", err.Error())
		err = nil
	}

	var activeBatches []Batch
	for _, batchShort := range batches {
		b.logger.Infof("Batch Name: %s, Status: %s", batchShort.Name, batchShort.Status)
		if batchShort.Status == Fermenting {
			batchId := batchShort.Id
			batch, err := b.GetBatch(ctx, batchId)
			if err != nil {
				return nil, err
			}

			activeBatches = append(activeBatches, *batch)
		}
	}
	span.SetAttributes(
		attribute.Int("brewfather.batches", len(batches)),
		attribute.Int("brewfather.active_batches", len(activeBatches)),
	)
	return activeBatches, nil
}

// @TODO(jtway) Currently there is only one webhook. Down the road I may
// need to change this up a bit
func (b *BrewfatherClient) GetWebhook() *BrewTrackerWebhook {
	for key, webhook := range b.webhooks {
		if webhook.config.Enabled {
			return webhook
		} else {
			fmt.Printf("Webhook %s is not enabled\n", key)
		}
	}
	return nil
}
