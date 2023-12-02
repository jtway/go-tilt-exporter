package brewfather

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	api_base_url = "https://api.brewfather.app/v2/"
)

type BrewfatherClient struct {
	client *http.Client
	config *Config

	webhooks map[string]*BrewTrackerWebhook
}

func NewBrewfatherClient(config *Config) *BrewfatherClient {

	brewClient := &BrewfatherClient{
		config:   config,
		webhooks: make(map[string]*BrewTrackerWebhook),
	}
	brewClient.client = &http.Client{
		Timeout: time.Second * 10,
	}

	for _, webhookConfig := range config.Webhooks {
		brewClient.webhooks[webhookConfig.Name] = NewBrewTrackerWebhook(&webhookConfig)
	}
	return brewClient
}

func (b *BrewfatherClient) GetBatches() ([]BatchShort, error) {
	url := api_base_url + "batches"

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

	var batches []BatchShort
	if err := json.Unmarshal(body, &batches); err != nil { // Parse []byte to the go struct pointer
		return nil, fmt.Errorf("Can not unmarshal JSON")
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

	return &batch, nil
}

func (b *BrewfatherClient) GetActiveBatches() ([]Batch, error) {
	batches, err := b.GetBatches()
	if err != nil {
		return nil, err
	}

	var activeBatches []Batch
	for _, batchShort := range batches {
		fmt.Printf("Batch Name: %s\n", batchShort.Name)
		fmt.Printf("Status %s\n", batchShort.Status)
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

func (b *BrewfatherClient) WebhookUpdate(beer string, gravity float32, temp float32) error {
	return nil
}
