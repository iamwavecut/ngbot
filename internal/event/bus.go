package event

import (
	"time"
)

type (
	bus struct {
		q chan Queueable
	}

	Queueable interface {
		Process()
		IsProcessed() bool
		Drop()
		IsDropped() bool
		Expired() bool
		Type() string
	}

	Base struct {
		processed bool
		dropped   bool
		expireAt  time.Time
		eventType string
	}
)

func CreateBase(eventType string, expiresAt time.Time) *Base {
	return &Base{
		expireAt:  expiresAt,
		eventType: eventType,
	}
}

func (b *Base) Process() {
	b.processed = true
}

func (b *Base) IsProcessed() bool {
	return b.processed
}

func (b *Base) Drop() {
	b.dropped = true
}

func (b *Base) IsDropped() bool {
	return b.dropped
}

func (b *Base) Expired() bool {
	return time.Until(b.expireAt) < 0
}

func (b *Base) Type() string {
	return b.eventType
}

var Bus = &bus{q: make(chan Queueable, 100000)}

// NQ adds an event to the queue
func (b *bus) NQ(event Queueable) {
	go func() { b.q <- event }()
}

// DQ returns the next event from the queue or nil if the queue is empty
func (b *bus) DQ() Queueable {
	select {
	case q := <-b.q:
		return q
	default:
		return nil
	}
}
