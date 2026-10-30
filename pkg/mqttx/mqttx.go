// Package mqttx creates the per-component MQTT clients. Each component gets
// its own client ID and credentials, so splitting a component into its own
// service later changes only where it runs.
package mqttx

import (
	"context"
	"fmt"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

// Config is the broker connection for one component.
type Config struct {
	Broker   string
	Username string
	Password string
	// Component names the client (go-tilt-exporter-<host>-<component>) and
	// labels the connected gauge.
	Component string
}

// Subscription is (re)established on every connect.
type Subscription struct {
	Filter  string
	Handler mqtt.MessageHandler
}

var connected = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "brewtracker",
	Subsystem: "mqtt",
	Name:      "component_connected",
	Help:      "1 while the component's MQTT client is connected.",
}, []string{"component"})

// Connect returns a client that reconnects forever and resubscribes on each
// connect. It waits up to 10s for the first connection but does not fail if
// the broker is down; the retry loop continues in the background.
func Connect(cfg Config, subs []Subscription, logger *zap.SugaredLogger) (mqtt.Client, error) {
	if cfg.Broker == "" {
		return nil, fmt.Errorf("%s: mqtt broker is required", cfg.Component)
	}
	host, _ := os.Hostname()
	clientID := fmt.Sprintf("go-tilt-exporter-%s-%s", host, cfg.Component)
	gauge := connected.WithLabelValues(cfg.Component)

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(clientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetOrderMatters(false).
		SetOnConnectHandler(func(c mqtt.Client) {
			gauge.Set(1)
			logger.Infof("%s: mqtt connected to %s", cfg.Component, cfg.Broker)
			for _, s := range subs {
				tok := c.Subscribe(s.Filter, 1, s.Handler)
				if tok.WaitTimeout(10*time.Second) && tok.Error() != nil {
					logger.Errorf("%s: subscribe %s failed: %s", cfg.Component, s.Filter, tok.Error())
				} else {
					logger.Infof("%s: subscribed to %s", cfg.Component, s.Filter)
				}
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			gauge.Set(0)
			logger.Warnf("%s: mqtt connection lost: %s", cfg.Component, err)
		})

	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		logger.Warnf("%s: mqtt not connected to %s yet; retrying in background", cfg.Component, cfg.Broker)
	} else if tok.Error() != nil {
		return nil, fmt.Errorf("%s: mqtt connect: %w", cfg.Component, tok.Error())
	}
	return c, nil
}

// Disconnect closes the client and clears the gauge.
func Disconnect(c mqtt.Client, component string) {
	if c != nil {
		c.Disconnect(250)
	}
	connected.WithLabelValues(component).Set(0)
}

// Extract continues a trace carried in a payload (contract.Trace.Carrier()).
func Extract(ctx context.Context, carrier map[string]string) context.Context {
	if carrier["traceparent"] == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}

// Inject returns the trace context of ctx as a carrier map.
func Inject(ctx context.Context) map[string]string {
	m := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, m)
	return m
}
