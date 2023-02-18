package event

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

type worker struct {
	subscriptions map[string][]func(event Queueable)
}

var (
	instance = &worker{
		subscriptions: map[string][]func(event Queueable){},
	}
	l = log.WithField("context", "event_worker")
)

func RunWorker() context.CancelFunc {
	ctx, cancelFunc := context.WithCancel(context.Background())
	instance.Run(ctx)
	return cancelFunc
}

func (w *worker) Run(ctx context.Context) {
	done := ctx.Done()
	toProfile := false
	profileTicker := time.NewTicker(time.Minute * 5)

	go func() {
		for {
			select {
			case <-done:
				// l.Info("shutting down event profile ticker by cancelled context")
				return
			case <-profileTicker.C:
				toProfile = true
			}
		}
	}()

	go func() {
		l.Trace("events runner go")
		var event Queueable
		for {
			select {
			case <-done:
				l.Info("shutting down event worker by cancelled context")
				return
			default:
				time.Sleep(1 * time.Millisecond)
				event = Bus.DQ()
				if event == nil {
					continue
				}

				if event.Expired() {
					// l.Trace("skip event ", event)
					continue
				}

				subscribers, ok := w.subscriptions[event.Type()]
				if !ok {
					// l.Trace("no event subs")
					Bus.NQ(event)
					continue
				}
				for _, sub := range subscribers {
					sub(event)
					if event.IsDropped() {
						// l.Trace("drop event after sub process", event)
						continue
					}
				}

				if event.IsDropped() {
					// l.Trace("drop event after all sub processed", event)
					continue
				}
				if !event.IsProcessed() {
					// l.Trace("unprocessed event, re-queueing ", event)
					Bus.NQ(event)
				}

				if qlen := len(Bus.q); toProfile && qlen > 0 {
					l.Debugf("unprocessed queue length: %d", qlen)
				}
			}
		}
	}()
}
