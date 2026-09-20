package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"testozon/internal/auth"
	"testozon/internal/domain"
	"testozon/internal/repository/memory"
	"testozon/internal/service"
	"testozon/internal/subscription"
)

func TestCommentAddedChecksPostAndStopsOnCancellation(t *testing.T) {
	repository := memory.New()
	broker := subscription.New(1)
	svc := service.New(repository, broker, func() time.Time {
		return time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	})
	resolver := &subscriptionResolver{&Resolver{Service: svc, Broker: broker}}

	if _, err := resolver.CommentAdded(t.Context(), uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing post error = %v, want ErrNotFound", err)
	}

	post, err := svc.CreatePost(auth.WithAuthorID(t.Context(), "author"), service.CreatePostInput{
		Title: "title", Content: "body",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	comments, err := resolver.CommentAdded(ctx, post.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "commenter", Text: "hello"}
	broker.Publish(want)

	select {
	case got := <-comments:
		if got == nil || got.ID != want.ID.String() || got.PostID != post.ID.String() {
			t.Fatalf("comment = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not deliver comment")
	}

	cancel()
	select {
	case _, ok := <-comments:
		if ok {
			t.Fatal("subscription channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription channel was not closed after cancellation")
	}
}
