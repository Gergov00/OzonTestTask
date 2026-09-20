package subscription

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"testozon/internal/domain"
)

func TestBrokerRoutesByPost(t *testing.T) {
	broker := New(1)
	wanted := uuid.New()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	comments := broker.Subscribe(ctx, wanted)

	broker.Publish(domain.Comment{ID: uuid.New(), PostID: uuid.New()})
	want := domain.Comment{ID: uuid.New(), PostID: wanted, Text: "wanted"}
	broker.Publish(want)

	select {
	case got := <-comments:
		if got != want {
			t.Fatalf("received comment = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("comment was not delivered")
	}
}

func TestBrokerClosesSubscriptionOnCancellation(t *testing.T) {
	broker := New(1)
	ctx, cancel := context.WithCancel(t.Context())
	postID := uuid.New()
	comments := broker.Subscribe(ctx, postID)

	cancel()

	select {
	case _, ok := <-comments:
		if ok {
			t.Fatal("subscription remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription was not closed after cancellation")
	}

	broker.Publish(domain.Comment{ID: uuid.New(), PostID: postID})
}

func TestBrokerPublishDoesNotBlockWhenBufferIsFull(t *testing.T) {
	broker := New(1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	postID := uuid.New()
	comments := broker.Subscribe(ctx, postID)
	broker.Publish(domain.Comment{ID: uuid.New(), PostID: postID})

	done := make(chan struct{})
	go func() {
		broker.Publish(domain.Comment{ID: uuid.New(), PostID: postID})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}

	select {
	case <-comments:
	default:
		t.Fatal("first buffered comment was unexpectedly dropped")
	}
}

func TestBrokerConcurrentPublishAndCancel(t *testing.T) {
	broker := New(4)
	postID := uuid.New()

	const subscriberCount = 100
	contexts := make([]context.CancelFunc, 0, subscriberCount)
	subscriptions := make([]<-chan domain.Comment, 0, subscriberCount)
	for range subscriberCount {
		ctx, cancel := context.WithCancel(t.Context())
		contexts = append(contexts, cancel)
		subscriptions = append(subscriptions, broker.Subscribe(ctx, postID))
	}

	start := make(chan struct{})
	var publishers sync.WaitGroup
	for range 8 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			<-start
			for range 1_000 {
				broker.Publish(domain.Comment{ID: uuid.New(), PostID: postID})
			}
		}()
	}

	close(start)
	for _, cancel := range contexts {
		cancel()
	}
	publishers.Wait()

	for _, comments := range subscriptions {
		for {
			select {
			case _, ok := <-comments:
				if !ok {
					goto closed
				}
			case <-time.After(time.Second):
				t.Fatal("subscription was not closed after concurrent cancellation")
			}
		}
	closed:
	}
}
