package bot

import (
	"context"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"
)

const (
	initialPollingBackoff = time.Second
	maxPollingBackoff     = 30 * time.Second
)

type PollingOptions struct {
	RequestTimeout time.Duration
	RecoveryWindow time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type PollingRecoveryError struct {
	Cause            error
	SinceLastHealthy time.Duration
}

func (e *PollingRecoveryError) Error() string {
	return fmt.Sprintf("polling recovery exhausted after %s: %v", e.SinceLastHealthy, e.Cause)
}

func (e *PollingRecoveryError) Unwrap() error {
	return e.Cause
}

func NewPollingOptions(requestTimeout, recoveryWindow time.Duration) PollingOptions {
	return PollingOptions{
		RequestTimeout: requestTimeout,
		RecoveryWindow: recoveryWindow,
		InitialBackoff: initialPollingBackoff,
		MaxBackoff:     maxPollingBackoff,
	}
}

type updateFetcher func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error)

func GetUpdatesChans(ctx context.Context, bot *api.BotAPI, config api.UpdateConfig, options PollingOptions) (api.UpdatesChannel, chan error) {
	return getUpdatesChansWithFetcher(ctx, bot.Buffer, config, normalizePollingOptions(options), bot.GetUpdatesWithContext)
}

func getUpdatesChansWithFetcher(ctx context.Context, buffer int, config api.UpdateConfig, options PollingOptions, fetch updateFetcher) (api.UpdatesChannel, chan error) {
	ch := make(chan api.Update, buffer)
	chErr := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(chErr)

		lastHealthyPollAt := time.Now()
		backoff := options.InitialBackoff

		for {
			if ctx.Err() != nil {
				return
			}

			requestCtx, cancel := context.WithTimeout(ctx, options.RequestTimeout)
			updates, err := fetch(requestCtx, config)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}

				sinceLastHealthy := time.Since(lastHealthyPollAt)
				log.WithError(err).WithFields(log.Fields{
					"backoff":            backoff,
					"since_last_healthy": sinceLastHealthy,
				}).Warn("Failed to poll updates")
				if sinceLastHealthy > options.RecoveryWindow {
					sendPollingError(chErr, &PollingRecoveryError{
						Cause:            err,
						SinceLastHealthy: sinceLastHealthy,
					})
					return
				}
				if !waitPollingBackoff(ctx, backoff) {
					return
				}
				backoff = nextPollingBackoff(backoff, options.MaxBackoff)
				continue
			}

			if len(updates) == 0 {
				lastHealthyPollAt = time.Now()
				backoff = options.InitialBackoff
				continue
			}

			healthyResponse := false
			droppedUpdates := 0

			for _, update := range updates {
				if update.UpdateID >= config.Offset {
					config.Offset = update.UpdateID + 1
				}
				if isStructurallyEmptyUpdate(&update) {
					droppedUpdates++
					log.WithField("update_id", update.UpdateID).Warn("Dropping empty update")
					continue
				}
				healthyResponse = true

				select {
				case ch <- update:
				case <-ctx.Done():
					return
				}
			}

			if healthyResponse {
				lastHealthyPollAt = time.Now()
				backoff = options.InitialBackoff
				continue
			}

			sinceLastHealthy := time.Since(lastHealthyPollAt)
			log.WithFields(log.Fields{
				"dropped_updates":    droppedUpdates,
				"backoff":            backoff,
				"since_last_healthy": sinceLastHealthy,
			}).Warn("Received malformed update batch")
			if sinceLastHealthy > options.RecoveryWindow {
				sendPollingError(chErr, &PollingRecoveryError{
					Cause:            fmt.Errorf("received malformed update batches for %s", sinceLastHealthy),
					SinceLastHealthy: sinceLastHealthy,
				})
				return
			}
			if !waitPollingBackoff(ctx, backoff) {
				return
			}
			backoff = nextPollingBackoff(backoff, options.MaxBackoff)
		}
	}()

	return ch, chErr
}

func normalizePollingOptions(options PollingOptions) PollingOptions {
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 75 * time.Second
	}
	if options.RecoveryWindow <= 0 {
		options.RecoveryWindow = 10 * time.Minute
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = initialPollingBackoff
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = maxPollingBackoff
	}
	if options.MaxBackoff < options.InitialBackoff {
		options.MaxBackoff = options.InitialBackoff
	}
	return options
}

func sendPollingError(ch chan error, err error) {
	select {
	case ch <- err:
	default:
	}
}

func waitPollingBackoff(ctx context.Context, backoff time.Duration) bool {
	timer := time.NewTimer(backoff)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextPollingBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}

func isStructurallyEmptyUpdate(u *api.Update) bool {
	if u == nil {
		return true
	}

	return u.Message == nil &&
		u.EditedMessage == nil &&
		u.ChannelPost == nil &&
		u.EditedChannelPost == nil &&
		u.BusinessConnection == nil &&
		u.BusinessMessage == nil &&
		u.EditedBusinessMessage == nil &&
		u.DeletedBusinessMessages == nil &&
		u.MessageReaction == nil &&
		u.MessageReactionCount == nil &&
		u.InlineQuery == nil &&
		u.ChosenInlineResult == nil &&
		u.CallbackQuery == nil &&
		u.ShippingQuery == nil &&
		u.PreCheckoutQuery == nil &&
		u.PurchasedPaidMedia == nil &&
		u.Poll == nil &&
		u.PollAnswer == nil &&
		u.MyChatMember == nil &&
		u.ChatMember == nil &&
		u.ChatJoinRequest == nil &&
		u.ChatBoost == nil &&
		u.ChatBoostRemoved == nil
}
