package observability

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

var (
	// Global logger instance
	Logger *zap.Logger

	// Metrics
	spamMessagesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "spam_messages_total",
			Help: "Total number of spam messages detected",
		},
		[]string{"type"},
	)

	messageProcessingDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "message_processing_duration_seconds",
			Help:    "Time spent processing messages",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)
)

func Init(ctx context.Context) error {
	// Initialize logger
	var err error
	Logger, err = zap.NewProduction()
	if err != nil {
		return err
	}

	// Register metrics
	prometheus.MustRegister(spamMessagesTotal)
	prometheus.MustRegister(messageProcessingDuration)

	// Setup OpenTelemetry (simplified setup)
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)

	// Start Prometheus metrics endpoint
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":2112", nil); err != nil {
			log.WithError(err).Error("metrics server failed")
		}
	}()

	return nil
}

// RecordSpamDetection records a spam message detection
func RecordSpamDetection(spamType string) {
	spamMessagesTotal.WithLabelValues(spamType).Inc()
}

// StartMessageProcessing returns a function to record message processing duration
func StartMessageProcessing() func(status string) {
	start := prometheus.NewTimer(messageProcessingDuration.WithLabelValues("processing"))
	return func(status string) {
		start.ObserveDuration()
	}
}
