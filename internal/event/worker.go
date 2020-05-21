package event

import (
	log "github.com/sirupsen/logrus"
	"sync"
	"time"
)

type worker struct {
	subscriptions map[string][]func(event Queuable)
}

var (
	once     sync.Once
	instance = &worker{
		subscriptions: map[string][]func(event Queuable){},
	}
	l = log.WithField("context", "event_worker")
)

func RunWorker() {
	instance.Run()
}

func (w *worker) Run() {
	go func() {
		l.Trace("events runner go")
		var event Queuable
		for {
			time.Sleep(1 * time.Millisecond)
			event = Bus.pop()
			if event == nil {
				continue
			}

			if event.Expired() {
				//l.Trace("drop event ", event)
				continue
			}
			subscribers, ok := w.subscriptions[event.Type()]
			if !ok {
				//l.Trace("no event subs")
				Bus.Enqueue(event)
				continue
			}
			for _, sub := range subscribers {
				sub(event)
				if event.IsDropped() {
					//l.Trace("drop event after sub process", event)
					continue
				}
			}

			if !event.IsProcessed() {
				//l.Trace("unprocessed event, re-queueing ", event)
				Bus.Enqueue(event)
			}
		}
	}()
}
