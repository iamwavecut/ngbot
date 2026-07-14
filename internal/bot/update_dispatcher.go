package bot

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"
)

var ErrDispatcherClosed = errors.New("update dispatcher is not accepting updates")

type UpdateProcessorFunc func(ctx context.Context, update *api.Update) error

type updateQueue struct {
	items   []api.Update
	running bool
}

type KeyedDispatcher struct {
	process      UpdateProcessorFunc
	logger       *log.Entry
	workerSlots  chan struct{}
	pendingSlots chan struct{}

	mu        sync.Mutex
	queues    map[string]*updateQueue
	runCtx    context.Context
	cancel    context.CancelFunc
	accepting bool
	wg        sync.WaitGroup
}

func NewKeyedDispatcher(process UpdateProcessorFunc, maxWorkers, pendingBudget int, logger *log.Entry) *KeyedDispatcher {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if pendingBudget < 1 {
		pendingBudget = 1
	}
	if logger == nil {
		logger = log.NewEntry(log.StandardLogger())
	}
	return &KeyedDispatcher{
		process:      process,
		logger:       logger,
		workerSlots:  make(chan struct{}, maxWorkers),
		pendingSlots: make(chan struct{}, pendingBudget),
		queues:       make(map[string]*updateQueue),
	}
}

func (d *KeyedDispatcher) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.accepting {
		return nil
	}
	d.runCtx, d.cancel = context.WithCancel(ctx)
	d.accepting = true
	return nil
}

func (d *KeyedDispatcher) Submit(ctx context.Context, update api.Update) error {
	d.mu.Lock()
	if !d.accepting || d.runCtx == nil {
		d.mu.Unlock()
		return ErrDispatcherClosed
	}
	runCtx := d.runCtx
	d.mu.Unlock()

	select {
	case d.pendingSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-runCtx.Done():
		return ErrDispatcherClosed
	}

	key := updateDispatchKey(&update)
	d.mu.Lock()
	if !d.accepting || d.runCtx != runCtx {
		d.mu.Unlock()
		<-d.pendingSlots
		return ErrDispatcherClosed
	}
	queue := d.queues[key]
	if queue == nil {
		queue = &updateQueue{}
		d.queues[key] = queue
	}
	queue.items = append(queue.items, update)
	if !queue.running {
		queue.running = true
		d.wg.Add(1)
		go d.runKey(runCtx, key)
	}
	d.mu.Unlock()
	return nil
}

func (d *KeyedDispatcher) Stop(ctx context.Context) error {
	d.mu.Lock()
	d.accepting = false
	cancel := d.cancel
	d.cancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.wg.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (d *KeyedDispatcher) runKey(runCtx context.Context, key string) {
	defer d.wg.Done()
	select {
	case d.workerSlots <- struct{}{}:
		defer func() { <-d.workerSlots }()
	case <-runCtx.Done():
		d.abandonKey(key)
		return
	}

	for {
		if runCtx.Err() != nil {
			d.abandonKey(key)
			return
		}
		update, ok := d.nextUpdate(key)
		if !ok {
			return
		}
		if runCtx.Err() == nil {
			d.processSafely(runCtx, key, &update)
		}
		<-d.pendingSlots
	}
}

func (d *KeyedDispatcher) nextUpdate(key string) (api.Update, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	queue := d.queues[key]
	if queue == nil || len(queue.items) == 0 {
		delete(d.queues, key)
		return api.Update{}, false
	}
	update := queue.items[0]
	queue.items[0] = api.Update{}
	queue.items = queue.items[1:]
	return update, true
}

func (d *KeyedDispatcher) abandonKey(key string) {
	d.mu.Lock()
	queue := d.queues[key]
	delete(d.queues, key)
	d.mu.Unlock()
	if queue == nil {
		return
	}
	for range len(queue.items) {
		<-d.pendingSlots
	}
}

func (d *KeyedDispatcher) processSafely(ctx context.Context, key string, update *api.Update) {
	defer func() {
		if recovered := recover(); recovered != nil {
			d.logger.WithFields(log.Fields{
				"dispatch_key": key,
				"update_id":    update.UpdateID,
				"panic":        recovered,
				"stack":        string(debug.Stack()),
			}).Error("update handler panicked")
		}
	}()
	if d.process == nil {
		return
	}
	if err := d.process(ctx, update); err != nil && !errors.Is(err, context.Canceled) {
		d.logger.WithError(err).WithFields(log.Fields{
			"dispatch_key": key,
			"update_id":    update.UpdateID,
		}).Error("failed to process update")
	}
}

func updateDispatchKey(update *api.Update) string {
	if chat := updateDispatchChat(update); chat != nil && chat.ID != 0 {
		return fmt.Sprintf("chat:%d", chat.ID)
	}
	if user := updateDispatchUser(update); user != nil && user.ID != 0 {
		return fmt.Sprintf("user:%d", user.ID)
	}
	return "global"
}

func updateDispatchChat(update *api.Update) *api.Chat {
	if update == nil {
		return nil
	}
	if chat := update.FromChat(); chat != nil {
		return chat
	}
	switch {
	case update.ChatJoinRequest != nil:
		return &update.ChatJoinRequest.Chat
	case update.MyChatMember != nil:
		return &update.MyChatMember.Chat
	case update.ChatMember != nil:
		return &update.ChatMember.Chat
	case update.MessageReaction != nil:
		return &update.MessageReaction.Chat
	default:
		return nil
	}
}

func updateDispatchUser(update *api.Update) *api.User {
	if update == nil {
		return nil
	}
	if user := update.SentFrom(); user != nil {
		return user
	}
	switch {
	case update.ChatJoinRequest != nil:
		return &update.ChatJoinRequest.From
	case update.MyChatMember != nil:
		return &update.MyChatMember.From
	case update.ChatMember != nil:
		return &update.ChatMember.From
	case update.MessageReaction != nil:
		return update.MessageReaction.User
	case update.PollAnswer != nil:
		return update.PollAnswer.User
	default:
		return nil
	}
}
