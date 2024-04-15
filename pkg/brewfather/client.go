package brewfather

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

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
	brewClient.client = &http.Client{
		Timeout: time.Second * 10,
	}

	for _, webhookConfig := range config.Webhooks {
		fmt.Printf("Creating webhook %s\n", webhookConfig.Name)
		brewClient.webhooks[webhookConfig.Name] = NewBrewTrackerWebhook(&webhookConfig)
	}
	return brewClient
}

func (b *BrewfatherClient) GetBatches() ([]BatchShort, error) {
	var batches []BatchShort
	url := api_base_url + "batches"

	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(b.config.UserId, b.config.ApiKey)

	for {
		response, err := b.client.Do(request)
		if err != nil {
			return batches, err
		}
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body) // response body is []byte

		var batchesPage []BatchShort
		if err := json.Unmarshal(body, &batchesPage); err != nil { // Parse []byte to the go struct pointer
			return batches, fmt.Errorf("Can not unmarshal JSON")
		}

		batchesReturned := len(batchesPage)
		b.logger.Infof("returned %d batches", batchesReturned)
		if batchesReturned == 0 {
			break
		}

		batches = append(batches, batchesPage...)

		queryValues := request.URL.Query()
		queryValues.Add("start_after", batchesPage[batchesReturned-1].Id)
		request.URL.RawQuery = queryValues.Encode()
		b.logger.Infof("attempting to get more batches")
	}
	return batches, nil
}

func (b *BrewfatherClient) GetBatch(batchId string) (*Batch, error) {
	url := api_base_url + "batches/" + batchId

	request, err := http.NewRequest("GET", url, nil)
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

	var batch Batch
	if err := json.Unmarshal(body, &batch); err != nil { // Parse []byte to the go struct pointer
		return nil, fmt.Errorf("Can not unmarshal JSON")
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

func (b *BrewfatherClient) GetActiveBatches() ([]Batch, error) {
	batches, err := b.GetBatches()
	if len(batches) == 0 && err != nil {
		return nil, err
	}
	if err != nil {
		b.logger.Infof("Batches were returned, but so was an error: %s", err.Error())
	}

	var activeBatches []Batch
	for _, batchShort := range batches {
		b.logger.Infof("Batch Name: %s, Status: %s", batchShort.Name, batchShort.Status)
		if batchShort.Status == Fermenting || batchShort.Status == Conditioning {
			batchId := batchShort.Id
			batch, err := b.GetBatch(batchId)
			if err != nil {
				return nil, err
			}

			activeBatches = append(activeBatches, *batch)
		}
	}
	return activeBatches, nil
}
