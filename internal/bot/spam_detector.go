package bot

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/iamwavecut/ngbot/internal/observability"
)

type SpamDetector struct {
	// existing fields...
}

func (d *SpamDetector) IsSpam(ctx context.Context, message string) bool {
	// Create span for tracing
	ctx, span := otel.Tracer("spam-detector").Start(ctx, "detect-spam")
	defer span.End()

	// Start timing the processing
	done := observability.StartMessageProcessing()
	defer done("completed")

	// Log the incoming message
	observability.Logger.Info("processing message for spam detection",
		zap.String("message", message),
	)

	isSpam := false // Your existing spam detection logic here

	if isSpam {
		observability.RecordSpamDetection("standard")
		observability.Logger.Warn("spam message detected",
			zap.String("message", message),
		)
	}

	return isSpam
}
