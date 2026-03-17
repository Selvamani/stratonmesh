package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Bus provides telemetry publishing and event streaming over NATS JetStream.
type Bus struct {
	conn   *nats.Conn
	js     nats.JetStreamContext
	logger *zap.SugaredLogger
}

// Config holds NATS connection settings.
type Config struct {
	URL string `yaml:"url" json:"url"` // e.g., "nats://localhost:4222"
}

// Event represents an orchestration event published to the bus.
type Event struct {
	Type      string      `json:"type"`      // deploy, scale, health, alert, snapshot, cost
	StackID   string      `json:"stackId"`
	Service   string      `json:"service,omitempty"`
	Severity  string      `json:"severity"` // info, success, warning, danger
	Message   string      `json:"message"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// MetricPoint is a single telemetry data point.
type MetricPoint struct {
	Node    string  `json:"node"`
	Service string  `json:"service,omitempty"`
	Stack   string  `json:"stack,omitempty"`
	Name    string  `json:"name"` // cpu, memory, gpu, latency_p99, request_rate
	Value   float64 `json:"value"`
	Unit    string  `json:"unit"` // percent, bytes, ms, req/s
	Time    time.Time `json:"time"`
}

// New connects to NATS and sets up JetStream streams.
func New(cfg Config, logger *zap.SugaredLogger) (*Bus, error) {
	if cfg.URL == "" {
		cfg.URL = "nats://localhost:4222"
	}

	conn, err := nats.Connect(cfg.URL,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warnw("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			logger.Info("NATS reconnected")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS %s: %w", cfg.URL, err)
	}

	js, err := conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("init JetStream: %w", err)
	}

	bus := &Bus{conn: conn, js: js, logger: logger}
	if err := bus.ensureStreams(); err != nil {
		return nil, err
	}

	logger.Infow("telemetry bus connected", "url", cfg.URL)
	return bus, nil
}

// Close shuts down the NATS connection.
func (b *Bus) Close() {
	b.conn.Close()
}

// ensureStreams creates the JetStream streams if they don't exist.
func (b *Bus) ensureStreams() error {
	streams := []struct {
		name     string
		subjects []string
		maxAge   time.Duration
	}{
		{"METRICS", []string{"telemetry.metrics.>", "telemetry.cost.>"}, 4 * time.Hour},
		{"LOGS", []string{"telemetry.logs.>"}, 24 * time.Hour},
		{"TRACES", []string{"telemetry.traces.>"}, 1 * time.Hour},
		{"EVENTS", []string{"orchestrator.events.>"}, 7 * 24 * time.Hour},
		{"DEADLETTER", []string{"telemetry.deadletter.>"}, 7 * 24 * time.Hour},
	}

	for _, s := range streams {
		_, err := b.js.AddStream(&nats.StreamConfig{
			Name:     s.name,
			Subjects: s.subjects,
			MaxAge:   s.maxAge,
			Replicas: 1, // Set to 3 in production
			Storage:  nats.FileStorage,
			Discard:  nats.DiscardOld,
		})
		if err != nil {
			// Stream may already exist — that's fine
			b.logger.Debugw("stream setup", "name", s.name, "result", err)
		}
	}

	return nil
}

// --- Publishing ---

// PublishEvent sends an orchestration event to the event stream.
func (b *Bus) PublishEvent(ctx context.Context, evt Event) error {
	evt.Timestamp = time.Now()
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("orchestrator.events.%s.%s", evt.StackID, evt.Type)
	_, err = b.js.Publish(subject, data)
	if err != nil {
		b.logger.Warnw("failed to publish event", "subject", subject, "error", err)
	}
	return err
}

// PublishMetric sends a metric data point to the metrics stream.
func (b *Bus) PublishMetric(ctx context.Context, m MetricPoint) error {
	m.Time = time.Now()
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("telemetry.metrics.%s.%s", m.Node, m.Service)
	_, err = b.js.Publish(subject, data)
	return err
}

// PublishLog sends a structured log entry.
func (b *Bus) PublishLog(ctx context.Context, node, service, level, message string) error {
	data, _ := json.Marshal(map[string]interface{}{
		"node": node, "service": service, "level": level, "message": message, "time": time.Now(),
	})
	subject := fmt.Sprintf("telemetry.logs.%s.%s", node, service)
	_, err := b.js.Publish(subject, data)
	return err
}

// --- Subscribing ---

// SubscribeEvents returns a channel of orchestration events.
func (b *Bus) SubscribeEvents(ctx context.Context, stackFilter string) (<-chan Event, error) {
	ch := make(chan Event, 128)

	subject := "orchestrator.events.>"
	if stackFilter != "" {
		subject = fmt.Sprintf("orchestrator.events.%s.>", stackFilter)
	}

	sub, err := b.js.Subscribe(subject, func(msg *nats.Msg) {
		var evt Event
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- evt:
		default:
			b.logger.Warn("event channel full, dropping")
		}
		msg.Ack()
	}, nats.Durable("events-consumer"), nats.DeliverNew())

	if err != nil {
		return nil, fmt.Errorf("subscribe events: %w", err)
	}

	go func() {
		<-ctx.Done()
		sub.Unsubscribe()
		close(ch)
	}()

	return ch, nil
}

// SubscribeMetrics returns a channel of metric points.
func (b *Bus) SubscribeMetrics(ctx context.Context, nodeFilter string) (<-chan MetricPoint, error) {
	ch := make(chan MetricPoint, 256)

	subject := "telemetry.metrics.>"
	if nodeFilter != "" {
		subject = fmt.Sprintf("telemetry.metrics.%s.>", nodeFilter)
	}

	sub, err := b.js.Subscribe(subject, func(msg *nats.Msg) {
		var m MetricPoint
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			return
		}
		select {
		case ch <- m:
		default:
		}
		msg.Ack()
	}, nats.Durable("metrics-consumer"), nats.DeliverNew())

	if err != nil {
		return nil, err
	}

	go func() {
		<-ctx.Done()
		sub.Unsubscribe()
		close(ch)
	}()

	return ch, nil
}
