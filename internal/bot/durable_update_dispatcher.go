package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const (
	defaultUpdateMaxAttempts    = 5
	defaultUpdateInitialBackoff = time.Second
	defaultUpdateMaxBackoff     = time.Minute
	completedUpdateRetention    = 7 * 24 * time.Hour
	failedUpdateRetention       = 30 * 24 * time.Hour
)

type DurableUpdateStore interface {
	EnqueueTelegramUpdate(ctx context.Context, update *db.TelegramUpdate) (bool, error)
	ListRunnableTelegramUpdates(ctx context.Context, now time.Time, limit int) ([]*db.TelegramUpdate, error)
	ClaimTelegramUpdate(ctx context.Context, updateID int, now time.Time) (*db.TelegramUpdate, bool, error)
	ScheduleTelegramUpdateRetry(ctx context.Context, updateID int, nextAttemptAt time.Time, source, lastError string) (bool, error)
	CompleteTelegramUpdate(ctx context.Context, updateID int, source string, now time.Time) (bool, error)
	DeadLetterTelegramUpdate(ctx context.Context, updateID int, source, reason, lastError string, now time.Time) (bool, error)
	RecoverTelegramUpdates(ctx context.Context, now time.Time) (int64, error)
	CleanupTelegramUpdates(ctx context.Context, completedBefore, deadLetterBefore time.Time) (int64, error)
}

type UpdateFailureDegrader func(ctx context.Context, update *api.Update, failure UpdateFailure) error

type DurableUpdateDispatcherOptions struct {
	MaxWorkers     int
	PendingBudget  int
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type DurableUpdateDispatcher struct {
	store    DurableUpdateStore
	process  UpdateProcessorFunc
	degrade  UpdateFailureDegrader
	options  DurableUpdateDispatcherOptions
	logger   *log.Entry
	inner    *KeyedDispatcher
	runCtx   context.Context
	cancel   context.CancelFunc
	timersWG sync.WaitGroup

	mu        sync.Mutex
	started   bool
	scheduled map[int]struct{}
}

func NewDurableUpdateDispatcher(
	store DurableUpdateStore,
	process UpdateProcessorFunc,
	degrade UpdateFailureDegrader,
	options DurableUpdateDispatcherOptions,
	logger *log.Entry,
) *DurableUpdateDispatcher {
	options = normalizeDurableUpdateDispatcherOptions(options)
	if logger == nil {
		logger = log.NewEntry(log.StandardLogger())
	}
	dispatcher := &DurableUpdateDispatcher{
		store:     store,
		process:   process,
		degrade:   degrade,
		options:   options,
		logger:    logger,
		scheduled: make(map[int]struct{}),
	}
	dispatcher.inner = NewKeyedDispatcher(dispatcher.execute, options.MaxWorkers, options.PendingBudget, logger)
	return dispatcher
}

func normalizeDurableUpdateDispatcherOptions(options DurableUpdateDispatcherOptions) DurableUpdateDispatcherOptions {
	if options.MaxWorkers < 1 {
		options.MaxWorkers = 1
	}
	if options.PendingBudget < 1 {
		options.PendingBudget = 1
	}
	if options.MaxAttempts < 1 {
		options.MaxAttempts = defaultUpdateMaxAttempts
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = defaultUpdateInitialBackoff
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = defaultUpdateMaxBackoff
	}
	if options.MaxBackoff < options.InitialBackoff {
		options.MaxBackoff = options.InitialBackoff
	}
	return options
}

func (d *DurableUpdateDispatcher) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return nil
	}
	if d.store == nil {
		d.mu.Unlock()
		return errors.New("durable update store is nil")
	}
	d.runCtx, d.cancel = context.WithCancel(ctx)
	d.started = true
	runCtx := d.runCtx
	d.mu.Unlock()

	if _, err := d.store.RecoverTelegramUpdates(runCtx, time.Now()); err != nil {
		d.resetStart()
		return fmt.Errorf("recover telegram updates: %w", err)
	}
	if _, err := d.store.CleanupTelegramUpdates(runCtx, time.Now().Add(-completedUpdateRetention), time.Now().Add(-failedUpdateRetention)); err != nil {
		d.resetStart()
		return fmt.Errorf("cleanup telegram updates: %w", err)
	}
	if err := d.inner.Start(runCtx); err != nil {
		d.resetStart()
		return fmt.Errorf("start keyed update dispatcher: %w", err)
	}
	if err := d.wakeRunnable(runCtx); err != nil {
		_ = d.inner.Stop(context.Background())
		d.resetStart()
		return fmt.Errorf("dispatch recovered telegram updates: %w", err)
	}
	return nil
}

func (d *DurableUpdateDispatcher) resetStart() {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel = nil
	d.runCtx = nil
	d.started = false
	d.mu.Unlock()
}

func (d *DurableUpdateDispatcher) Persist(ctx context.Context, update api.Update) error {
	payload, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("encode telegram update %d: %w", update.UpdateID, err)
	}
	_, err = d.store.EnqueueTelegramUpdate(ctx, &db.TelegramUpdate{
		UpdateID:         update.UpdateID,
		DispatchKey:      updateDispatchKey(&update),
		Payload:          payload,
		SecurityRelevant: isSecurityRelevantUpdate(&update),
		ReceivedAt:       time.Now(),
	})
	if err != nil {
		return fmt.Errorf("persist telegram update %d: %w", update.UpdateID, err)
	}
	return nil
}

func (d *DurableUpdateDispatcher) Submit(ctx context.Context, update api.Update) error {
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return ErrDispatcherClosed
	}
	if _, exists := d.scheduled[update.UpdateID]; exists {
		d.mu.Unlock()
		return nil
	}
	d.scheduled[update.UpdateID] = struct{}{}
	d.mu.Unlock()
	if err := d.inner.Submit(ctx, update); err != nil {
		d.removeScheduled(update.UpdateID)
		return err
	}
	return nil
}

func (d *DurableUpdateDispatcher) Stop(ctx context.Context) error {
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil
	}
	d.started = false
	cancel := d.cancel
	d.cancel = nil
	d.runCtx = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	innerErr := d.inner.Stop(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.timersWG.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return innerErr
	}
}

func (d *DurableUpdateDispatcher) execute(ctx context.Context, submitted *api.Update) error {
	if submitted == nil {
		return nil
	}
	record, claimed, err := d.store.ClaimTelegramUpdate(ctx, submitted.UpdateID, time.Now())
	if err != nil {
		d.removeScheduled(submitted.UpdateID)
		return err
	}
	if !claimed {
		d.removeScheduled(submitted.UpdateID)
		return nil
	}

	var update api.Update
	if err := json.Unmarshal(record.Payload, &update); err != nil {
		failure := ClassifyUpdateFailure(NewTerminalUpdateFailure(UpdateFailurePayload, "malformed_payload", err))
		return d.finishFailure(ctx, record, &update, failure)
	}
	if isStructurallyEmptyUpdate(&update) {
		failure := ClassifyUpdateFailure(NewTerminalUpdateFailure(UpdateFailurePayload, "malformed_update", errors.New("telegram update has no supported body")))
		return d.finishFailure(ctx, record, &update, failure)
	}

	processErr := d.callProcessor(ctx, &update)
	if processErr == nil {
		changed, completeErr := d.store.CompleteTelegramUpdate(ctx, record.UpdateID, "handler", time.Now())
		d.removeScheduled(record.UpdateID)
		if completeErr != nil {
			return completeErr
		}
		if !changed {
			return fmt.Errorf("telegram update %d completion fence was lost", record.UpdateID)
		}
		d.scheduleWake()
		return nil
	}
	return d.finishFailure(ctx, record, &update, ClassifyUpdateFailure(processErr))
}

func (d *DurableUpdateDispatcher) callProcessor(ctx context.Context, update *api.Update) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = NewTerminalUpdateFailure(
				UpdateFailureRuntime,
				"ambiguous_handler_panic",
				fmt.Errorf("panic: %v\n%s", recovered, debug.Stack()),
			)
		}
	}()
	if d.process == nil {
		return nil
	}
	return d.process(ctx, update)
}

func (d *DurableUpdateDispatcher) finishFailure(ctx context.Context, record *db.TelegramUpdate, update *api.Update, failure UpdateFailure) error {
	lastError := failure.Error()
	if failure.Cause != nil {
		lastError = failure.Cause.Error()
	}
	if failure.Disposition == UpdateFailureRetryable && record.AttemptCount < d.options.MaxAttempts {
		nextAttemptAt := time.Now().Add(d.retryDelay(record.AttemptCount))
		changed, err := d.store.ScheduleTelegramUpdateRetry(ctx, record.UpdateID, nextAttemptAt, string(failure.Source), lastError)
		d.removeScheduled(record.UpdateID)
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("telegram update %d retry fence was lost", record.UpdateID)
		}
		d.scheduleRetry(update, nextAttemptAt)
		return nil
	}

	reason := failure.Reason
	if failure.Disposition == UpdateFailureRetryable {
		reason = "retry_exhausted"
		if d.degrade != nil {
			if err := d.degrade(ctx, update, failure); err != nil {
				lastError = errors.Join(failure.Cause, fmt.Errorf("safe degradation: %w", err)).Error()
			}
		}
	}
	changed, err := d.store.DeadLetterTelegramUpdate(ctx, record.UpdateID, string(failure.Source), reason, lastError, time.Now())
	d.removeScheduled(record.UpdateID)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("telegram update %d dead-letter fence was lost", record.UpdateID)
	}
	d.logger.WithFields(log.Fields{
		logFieldUpdateID:    record.UpdateID,
		"failure_source":    failure.Source,
		"failure_reason":    reason,
		"security_relevant": record.SecurityRelevant,
	}).WithError(failure.Cause).Error("telegram update moved to durable failure ledger")
	d.scheduleWake()
	return nil
}

func (d *DurableUpdateDispatcher) retryDelay(attempt int) time.Duration {
	delay := d.options.InitialBackoff
	for range max(attempt-1, 0) {
		if delay >= d.options.MaxBackoff/2 {
			return d.options.MaxBackoff
		}
		delay *= 2
	}
	return min(delay, d.options.MaxBackoff)
}

func (d *DurableUpdateDispatcher) scheduleRetry(update *api.Update, at time.Time) {
	if update == nil {
		return
	}
	d.mu.Lock()
	runCtx := d.runCtx
	started := d.started
	d.mu.Unlock()
	if !started || runCtx == nil {
		return
	}
	copyUpdate := *update
	d.timersWG.Go(func() {
		timer := time.NewTimer(max(time.Until(at), 0))
		defer timer.Stop()
		select {
		case <-runCtx.Done():
			return
		case <-timer.C:
			if err := d.Submit(runCtx, copyUpdate); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrDispatcherClosed) {
				d.logger.WithError(err).WithField(logFieldUpdateID, copyUpdate.UpdateID).Error("failed to resubmit durable telegram update")
			}
		}
	})
}

func (d *DurableUpdateDispatcher) scheduleWake() {
	d.mu.Lock()
	runCtx := d.runCtx
	started := d.started
	d.mu.Unlock()
	if !started || runCtx == nil {
		return
	}
	d.timersWG.Go(func() {
		if err := d.wakeRunnable(runCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrDispatcherClosed) {
			d.logger.WithError(err).Error("failed to wake durable telegram updates")
		}
	})
}

func (d *DurableUpdateDispatcher) wakeRunnable(ctx context.Context) error {
	updates, err := d.store.ListRunnableTelegramUpdates(ctx, time.Now(), d.options.PendingBudget)
	if err != nil {
		return err
	}
	for _, record := range updates {
		var update api.Update
		if err := json.Unmarshal(record.Payload, &update); err != nil {
			update.UpdateID = record.UpdateID
		}
		if err := d.Submit(ctx, update); err != nil {
			return err
		}
	}
	return nil
}

func (d *DurableUpdateDispatcher) removeScheduled(updateID int) {
	d.mu.Lock()
	delete(d.scheduled, updateID)
	d.mu.Unlock()
}

func isSecurityRelevantUpdate(update *api.Update) bool {
	if update == nil {
		return false
	}
	return update.Message != nil ||
		update.EditedMessage != nil ||
		update.ChannelPost != nil ||
		update.EditedChannelPost != nil ||
		update.MessageReaction != nil ||
		update.CallbackQuery != nil ||
		update.MyChatMember != nil ||
		update.ChatMember != nil ||
		update.ChatJoinRequest != nil
}
