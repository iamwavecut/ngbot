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
	return b.expireAt.Sub(time.Now()) < 0
}

func (b *Base) Type() string {
	return b.eventType
}

var Bus = &bus{q: make(chan Queueable, 100000)}

func (b *bus) Enqueue(event Queueable) {
	go func() { b.q <- event }()
}

func (b *bus) pop() Queueable {
	select {
	case q := <-b.q:
		return q
	default:
		return nil
	}
}
