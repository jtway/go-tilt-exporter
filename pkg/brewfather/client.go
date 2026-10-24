package brewfather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

const (
	api_base_url = "https://api.brewfather.app/v2/"
	tracerName   = "github.com/jtway/go-tilt-exporter/pkg/brewfather"
	pageLimit    = 50 // API max
)

// BrewfatherClient is the API client. Only batch-sync should use it: the API
// allows 500 calls/hour per key.
type BrewfatherClient struct {
	client *http.Client
	logger *zap.SugaredLogger
	config *Config
}

// NewHTTPClient returns an HTTP client whose calls produce client spans.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}

func NewBrewfatherClient(config *Config, logger *zap.SugaredLogger) *BrewfatherClient {
	return &BrewfatherClient{config: config, logger: logger, client: NewHTTPClient()}
}

func (b *BrewfatherClient) baseURL() string {
	if b.config.ApiBaseUrl != "" {
		return strings.TrimSuffix(b.config.ApiBaseUrl, "/") + "/"
	}
	return api_base_url
}

func (b *BrewfatherClient) get(ctx context.Context, path string, q url.Values, out any) error {
	u := b.baseURL() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(b.config.UserId, b.config.ApiKey)
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("brewfather GET %s: %s", path, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("brewfather GET %s: decode: %w", path, err)
	}
	return nil
}

// GetBatchesByStatus lists batches in one status, paging 50 at a time.
func (b *BrewfatherClient) GetBatchesByStatus(ctx context.Context, status Status) ([]BatchShort, error) {
	var all []BatchShort
	q := url.Values{"status": {string(status)}, "limit": {fmt.Sprint(pageLimit)}}
	for {
		var page []BatchShort
		if err := b.get(ctx, "batches", q, &page); err != nil {
			return all, err
		}
		// Belt and braces: don't trust the filter blindly.
		for _, p := range page {
			if p.Status == status {
				all = append(all, p)
			}
		}
		if len(page) < pageLimit {
			return all, nil
		}
		q.Set("start_after", page[len(page)-1].Id)
	}
}

// GetBatch fetches one full batch (devices, recipe, measured values).
func (b *BrewfatherClient) GetBatch(ctx context.Context, batchId string) (*Batch, error) {
	var batch Batch
	if err := b.get(ctx, "batches/"+url.PathEscape(batchId), nil, &batch); err != nil {
		return nil, err
	}
	return &batch, nil
}

// GetActiveBatches returns full batches in any of the given statuses. It
// costs one list call per status plus one call per batch found.
func (b *BrewfatherClient) GetActiveBatches(ctx context.Context, statuses []Status) (_ []Batch, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "brewfather.get_active_batches")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	var active []Batch
	for _, st := range statuses {
		shorts, err := b.GetBatchesByStatus(ctx, st)
		if err != nil {
			return nil, err
		}
		for _, s := range shorts {
			batch, err := b.GetBatch(ctx, s.Id)
			if err != nil {
				return nil, err
			}
			active = append(active, *batch)
		}
	}
	span.SetAttributes(attribute.Int("brewfather.active_batches", len(active)))
	return active, nil
}
