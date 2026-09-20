package subscription

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"testozon/internal/domain"
)

type Broker struct {
	mu          sync.RWMutex
	buffer      int
	subscribers map[uuid.UUID]map[chan domain.Comment]struct{}
}

func New(buffer int) *Broker {
	return &Broker{
		buffer:      buffer,
		subscribers: make(map[uuid.UUID]map[chan domain.Comment]struct{}),
	}
}

func (b *Broker) Subscribe(ctx context.Context, postID uuid.UUID) <-chan domain.Comment {
	comments := make(chan domain.Comment, b.buffer)

	b.mu.Lock()
	if b.subscribers[postID] == nil {
		b.subscribers[postID] = make(map[chan domain.Comment]struct{})
	}
	b.subscribers[postID][comments] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.unsubscribe(postID, comments)
	}()

	return comments
}

func (b *Broker) Publish(comment domain.Comment) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for subscriber := range b.subscribers[comment.PostID] {
		select {
		case subscriber <- comment:
		default:
		}
	}
}

func (b *Broker) unsubscribe(postID uuid.UUID, comments chan domain.Comment) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subscribers := b.subscribers[postID]
	if _, ok := subscribers[comments]; !ok {
		return
	}
	delete(subscribers, comments)
	if len(subscribers) == 0 {
		delete(b.subscribers, postID)
	}
	close(comments)
}
