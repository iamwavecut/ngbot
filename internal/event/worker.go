package event

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

type worker struct {
	subscriptions map[string][]func(event Queueable)
	logger        *log.Entry
}

var instance = &worker{
	subscriptions: map[string][]func(event Queueable){},
	logger:        log.WithField("context", "event_worker"),
}

func RunWorker() context.CancelFunc {
	ctx, cancelFunc := context.WithCancel(context.Background())
	instance.Run(ctx)
	return cancelFunc
}

func (w *worker) Run(ctx context.Context) {
	if w.logger == nil {
		w.logger = log.WithField("context", "event_worker")
	}

	done := ctx.Done()
	toProfile := false
	profileTicker := time.NewTicker(time.Minute * 5)

	go func() {
		for {
			select {
			case <-done:
				return
			case <-profileTicker.C:
				toProfile = true
			}
		}
	}()

	go func() {
		w.logger.Trace("events runner go")
		var event Queueable
		for {
			select {
			case <-done:
				w.logger.Info("shutting down event worker by cancelled context")
				return
			default:
				time.Sleep(1 * time.Millisecond)
				event = Bus.DQ()
				if event == nil {
					continue
				}

				if event.Expired() {
					continue
				}

				subscribers, ok := w.subscriptions[event.Type()]
				if !ok {
					Bus.NQ(event)
					continue
				}
				for _, sub := range subscribers {
					sub(event)
					if event.IsDropped() {
						continue
					}
				}

				if event.IsDropped() {
					continue
				}
				if !event.IsProcessed() {
					Bus.NQ(event)
				}

				if qLen := len(Bus.q); toProfile && qLen > 0 {
					w.logger.WithField("queue_length", qLen).Debug("unprocessed queue length")
				}
			}
		}
	}()
}
