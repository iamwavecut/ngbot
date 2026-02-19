package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/iamwavecut/ngbot/internal/db"
)

type reactorStoreStub struct {
	examples []*db.ChatSpamExample
	err      error
}

func (s *reactorStoreStub) ListChatSpamExamples(_ context.Context, _ int64, _ int, _ int) ([]*db.ChatSpamExample, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.examples, nil
}

func TestLoadSpamExamplesReturnsTrimmedEntries(t *testing.T) {
	t.Parallel()

	r := &Reactor{
		store: &reactorStoreStub{
			examples: []*db.ChatSpamExample{
				{Text: " spam one "},
				{Text: ""},
				{Text: "   "},
				{Text: "\nspam two\t"},
			},
		},
	}

	examples := r.loadSpamExamples(context.Background(), 100)
	if len(examples) != 2 {
		t.Fatalf("expected 2 examples, got %d (%v)", len(examples), examples)
	}
	if examples[0] != "spam one" {
		t.Fatalf("unexpected first example: %q", examples[0])
	}
	if examples[1] != "spam two" {
		t.Fatalf("unexpected second example: %q", examples[1])
	}
}

func TestLoadSpamExamplesReturnsNilOnStoreError(t *testing.T) {
	t.Parallel()

	r := &Reactor{
		store: &reactorStoreStub{err: errors.New("db failure")},
	}

	examples := r.loadSpamExamples(context.Background(), 100)
	if examples != nil {
		t.Fatalf("expected nil examples on store error, got %v", examples)
	}
}
