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
	"github.com/pborman/uuid"
	log "github.com/sirupsen/logrus"
)

const (
	defaultUpdateMaxAttempts    = 5
	defaultUpdateInitialBackoff = time.Second
	defaultUpdateMaxBackoff     = time.Minute
	completedUpdateRetention    = 7 * 24 * time.Hour
	failedUpdateRetention       = 30 * 24 * time.Hour
	defaultSchedulerBackoff     = time.Second
	defaultProcessingTimeout    = 5 * time.Minute
	defaultRecoveryInterval     = time.Minute
	defaultCleanupInterval      = 24 * time.Hour
)

type DurableUpdateStore interface {
	EnqueueTelegramUpdate(ctx context.Context, update *db.TelegramUpdate) (bool, error)
	ListRunnableTelegramUpdates(ctx context.Context, now time.Time, limit int) ([]*db.TelegramUpdate, error)
	NextTelegramUpdateAvailableAt(ctx context.Context) (time.Time, bool, error)
	ClaimTelegramUpdate(ctx context.Context, updateID int, owner string, now, leaseUntil time.Time) (*db.TelegramUpdate, bool, error)
	RenewTelegramUpdateLease(ctx context.Context, updateID int, owner string, version int64, leaseUntil, now time.Time) (bool, error)
	ScheduleTelegramUpdateRetry(ctx context.Context, updateID int, owner string, version int64, nextAttemptAt time.Time, source, lastError string) (bool, error)
	CompleteTelegramUpdate(ctx context.Context, updateID int, owner string, version int64, source string, now time.Time) (bool, error)
	DeadLetterTelegramUpdate(ctx context.Context, updateID int, owner string, version int64, source, reason, lastError string, now time.Time) (bool, error)
	RecoverTelegramUpdates(ctx context.Context, now time.Time) (int64, error)
	RecoverStaleTelegramUpdates(ctx context.Context, now time.Time) (int64, error)
	CleanupTelegramUpdates(ctx context.Context, completedBefore, deadLetterBefore time.Time) (int64, error)
}

type UpdateFailureDegrader func(ctx context.Context, update *api.Update, failure UpdateFailure) error

type DurableUpdateDispatcherOptions struct {
	MaxWorkers        int
	PendingBudget     int
	MaxAttempts       int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	SchedulerBackoff  time.Duration
	ProcessingTimeout time.Duration
	RecoveryInterval  time.Duration
	CleanupInterval   time.Duration
}

type DurableUpdateDispatcher struct {
	store       DurableUpdateStore
	process     UpdateProcessorFunc
	degrade     UpdateFailureDegrader
	options     DurableUpdateDispatcherOptions
	logger      *log.Entry
	inner       *KeyedDispatcher
	runCtx      context.Context
	cancel      context.CancelFunc
	schedulerWG sync.WaitGroup
	wake        chan struct{}

	mu             sync.Mutex
	started        bool
	scheduled      map[int]struct{}
	retryNotBefore time.Time
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
		wake:      make(chan struct{}, 1),
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
	if options.SchedulerBackoff <= 0 {
		options.SchedulerBackoff = defaultSchedulerBackoff
	}
	if options.ProcessingTimeout <= 0 {
		options.ProcessingTimeout = defaultProcessingTimeout
	}
	if options.RecoveryInterval <= 0 {
		options.RecoveryInterval = defaultRecoveryInterval
	}
	if options.CleanupInterval <= 0 {
		options.CleanupInterval = defaultCleanupInterval
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
	d.schedulerWG.Go(func() { d.runScheduler(runCtx) })
	d.notifyScheduler()
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
	d.notifyScheduler()
	return nil
}

func (d *DurableUpdateDispatcher) Submit(ctx context.Context, update api.Update) error {
	_ = update
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return ErrDispatcherClosed
	}
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	d.notifyScheduler()
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
		d.schedulerWG.Wait()
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
	now := time.Now()
	owner := uuid.New()
	record, claimed, err := d.store.ClaimTelegramUpdate(ctx, submitted.UpdateID, owner, now, now.Add(d.options.ProcessingTimeout))
	if err != nil {
		d.removeScheduled(submitted.UpdateID)
		d.deferScheduler()
		return err
	}
	if !claimed {
		d.removeScheduled(submitted.UpdateID)
		return nil
	}

	processCtx, cancelProcess := context.WithCancel(ctx)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan error, 1)
	go d.heartbeatLease(processCtx, cancelProcess, record, stopHeartbeat, heartbeatDone)

	var update api.Update
	var processErr error
	if err := json.Unmarshal(record.Payload, &update); err != nil {
		processErr = NewTerminalUpdateFailure(UpdateFailurePayload, "malformed_payload", err)
	} else if isStructurallyEmptyUpdate(&update) {
		processErr = NewTerminalUpdateFailure(UpdateFailurePayload, "malformed_update", errors.New("telegram update has no supported body"))
	} else {
		processErr = d.callProcessor(processCtx, &update)
	}

	var outcomeErr error
	if processErr == nil {
		changed, completeErr := d.retryStoreTransition(processCtx, func() (bool, error) {
			return d.store.CompleteTelegramUpdate(processCtx, record.UpdateID, record.LeaseOwner, record.LeaseVersion, "handler", time.Now())
		})
		d.removeScheduled(record.UpdateID)
		if completeErr != nil {
			d.deferScheduler()
			outcomeErr = completeErr
		} else if !changed {
			outcomeErr = fmt.Errorf("telegram update %d completion fence was lost", record.UpdateID)
		} else {
			d.notifyScheduler()
		}
	} else {
		outcomeErr = d.finishFailure(processCtx, record, &update, ClassifyUpdateFailure(processErr))
	}
	cancelProcess()
	close(stopHeartbeat)
	heartbeatErr := <-heartbeatDone
	if outcomeErr == nil {
		return nil
	}
	if heartbeatErr != nil {
		return errors.Join(outcomeErr, heartbeatErr)
	}
	return outcomeErr
}

func (d *DurableUpdateDispatcher) heartbeatLease(ctx context.Context, cancelProcess context.CancelFunc, record *db.TelegramUpdate, stop <-chan struct{}, done chan<- error) {
	interval := max(d.options.ProcessingTimeout/3, time.Millisecond)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	leaseUntil := record.LeaseUntil.Time
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-stop:
			done <- nil
			return
		case <-ticker.C:
			var renewedUntil time.Time
			renewed, err := d.retryStoreTransitionUntil(ctx, leaseUntil, func() (bool, error) {
				now := time.Now()
				renewedUntil = now.Add(d.options.ProcessingTimeout)
				return d.store.RenewTelegramUpdateLease(ctx, record.UpdateID, record.LeaseOwner, record.LeaseVersion, renewedUntil, now)
			})
			if err != nil || !renewed {
				cancelProcess()
				if err == nil {
					err = errors.New("telegram update lease ownership was lost")
				}
				done <- NewTerminalUpdateFailure(UpdateFailureRuntime, "ambiguous_handler_lease_lost", err)
				return
			}
			leaseUntil = renewedUntil
		}
	}
}

func (d *DurableUpdateDispatcher) retryStoreTransitionUntil(ctx context.Context, deadline time.Time, transition func() (bool, error)) (bool, error) {
	backoff := d.options.SchedulerBackoff
	for {
		if !time.Now().Before(deadline) {
			return false, context.DeadlineExceeded
		}
		changed, err := transition()
		if err == nil {
			return changed, nil
		}
		failure := ClassifyUpdateFailure(err)
		if failure.Source != UpdateFailureSQLite || failure.Disposition != UpdateFailureRetryable {
			return false, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, errors.Join(err, context.DeadlineExceeded)
		}
		timer := time.NewTimer(min(backoff, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		backoff = min(backoff*2, d.options.MaxBackoff)
	}
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
		changed, err := d.retryStoreTransition(ctx, func() (bool, error) {
			return d.store.ScheduleTelegramUpdateRetry(ctx, record.UpdateID, record.LeaseOwner, record.LeaseVersion, nextAttemptAt, string(failure.Source), lastError)
		})
		d.removeScheduled(record.UpdateID)
		if err != nil {
			d.deferScheduler()
			return err
		}
		if !changed {
			return fmt.Errorf("telegram update %d retry fence was lost", record.UpdateID)
		}
		d.notifyScheduler()
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
	changed, err := d.retryStoreTransition(ctx, func() (bool, error) {
		return d.store.DeadLetterTelegramUpdate(ctx, record.UpdateID, record.LeaseOwner, record.LeaseVersion, string(failure.Source), reason, lastError, time.Now())
	})
	d.removeScheduled(record.UpdateID)
	if err != nil {
		d.deferScheduler()
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
	}).Error("telegram update moved to durable failure ledger")
	d.notifyScheduler()
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

func (d *DurableUpdateDispatcher) dispatchRunnable(ctx context.Context) (int, bool, error) {
	updates, err := d.store.ListRunnableTelegramUpdates(ctx, time.Now(), d.options.PendingBudget)
	if err != nil {
		return 0, false, err
	}
	dispatched := 0
	for _, record := range updates {
		if !d.markScheduled(record.UpdateID) {
			continue
		}
		var update api.Update
		if err := json.Unmarshal(record.Payload, &update); err != nil {
			update.UpdateID = record.UpdateID
		}
		if err := d.inner.Submit(ctx, update); err != nil {
			d.removeScheduled(record.UpdateID)
			return dispatched, true, err
		}
		dispatched++
	}
	return dispatched, len(updates) > 0, nil
}

func (d *DurableUpdateDispatcher) runScheduler(ctx context.Context) {
	nextRecovery := time.Now().Add(d.options.RecoveryInterval)
	nextCleanup := time.Now().Add(d.options.CleanupInterval)
	for {
		now := time.Now()
		if !now.Before(nextRecovery) {
			if _, err := d.store.RecoverStaleTelegramUpdates(ctx, now); err != nil && ctx.Err() == nil {
				d.logger.WithError(err).Error("failed to recover stale telegram updates")
				d.deferScheduler()
			}
			nextRecovery = now.Add(d.options.RecoveryInterval)
		}
		if !now.Before(nextCleanup) {
			if _, err := d.store.CleanupTelegramUpdates(ctx, now.Add(-completedUpdateRetention), now.Add(-failedUpdateRetention)); err != nil && ctx.Err() == nil {
				d.logger.WithError(err).Error("failed to clean up telegram updates")
				d.deferScheduler()
			}
			nextCleanup = now.Add(d.options.CleanupInterval)
		}

		dispatched, dueScheduled, err := d.dispatchRunnable(ctx)
		if err != nil && ctx.Err() == nil && !errors.Is(err, ErrDispatcherClosed) {
			d.logger.WithError(err).Error("failed to dispatch durable telegram updates")
			d.deferScheduler()
		}
		waitUntil := minTime(nextRecovery, nextCleanup)
		if err == nil && dispatched > 0 {
			waitUntil = time.Now()
		} else if err == nil && !dueScheduled {
			if next, ok, nextErr := d.store.NextTelegramUpdateAvailableAt(ctx); nextErr != nil {
				if ctx.Err() == nil {
					d.logger.WithError(nextErr).Error("failed to read next telegram update availability")
					d.deferScheduler()
				}
			} else if ok {
				waitUntil = minTime(waitUntil, next)
			}
		}
		d.mu.Lock()
		if d.retryNotBefore.After(waitUntil) || waitUntil.IsZero() {
			waitUntil = d.retryNotBefore
		}
		d.mu.Unlock()
		if !d.waitForScheduler(ctx, waitUntil) {
			return
		}
	}
}

func (d *DurableUpdateDispatcher) waitForScheduler(ctx context.Context, until time.Time) bool {
	delay := max(time.Until(until), 0)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-d.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (d *DurableUpdateDispatcher) notifyScheduler() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *DurableUpdateDispatcher) deferScheduler() {
	d.mu.Lock()
	d.retryNotBefore = time.Now().Add(d.options.SchedulerBackoff)
	d.mu.Unlock()
	d.notifyScheduler()
}

func (d *DurableUpdateDispatcher) retryStoreTransition(ctx context.Context, transition func() (bool, error)) (bool, error) {
	backoff := d.options.SchedulerBackoff
	for {
		changed, err := transition()
		if err == nil {
			return changed, nil
		}
		failure := ClassifyUpdateFailure(err)
		if failure.Source != UpdateFailureSQLite || failure.Disposition != UpdateFailureRetryable {
			return false, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		backoff = min(backoff*2, d.options.MaxBackoff)
	}
}

func (d *DurableUpdateDispatcher) markScheduled(updateID int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.scheduled[updateID]; exists {
		return false
	}
	d.scheduled[updateID] = struct{}{}
	return true
}

func minTime(first, second time.Time) time.Time {
	if first.IsZero() || (!second.IsZero() && second.Before(first)) {
		return second
	}
	return first
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
