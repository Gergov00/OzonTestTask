package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"testozon/internal/auth"
	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
	"testozon/internal/repository/memory"
	"testozon/internal/subscription"
)

var fixedTime = time.Date(2026, 2, 3, 4, 5, 6, 7, time.FixedZone("test", 3*60*60))

func fixedClock() time.Time { return fixedTime }

func TestService_CreatePost(t *testing.T) {
	t.Run("uses context author and UTC clock", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		post, err := svc.CreatePost(auth.WithAuthorID(t.Context(), "author-1"), CreatePostInput{
			Title:   "title",
			Content: "body",
		})
		if err != nil {
			t.Fatalf("CreatePost() error = %v", err)
		}
		if post.ID == uuid.Nil {
			t.Fatal("CreatePost() returned nil UUID")
		}
		if post.AuthorID != "author-1" {
			t.Fatalf("AuthorID = %q, want %q", post.AuthorID, "author-1")
		}
		if !post.CommentsEnabled {
			t.Fatal("CommentsEnabled = false, want true")
		}
		if !post.CreatedAt.Equal(fixedTime.UTC()) || post.CreatedAt.Location() != time.UTC {
			t.Fatalf("CreatedAt = %v, want %v in UTC", post.CreatedAt, fixedTime.UTC())
		}
	})

	t.Run("rejects missing author", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		_, err := svc.CreatePost(t.Context(), CreatePostInput{Title: "title", Content: "body"})
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("CreatePost() error = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("accepts title at 200 Unicode characters", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		_, err := svc.CreatePost(auth.WithAuthorID(t.Context(), "author"), CreatePostInput{
			Title:   strings.Repeat("я", 200),
			Content: "body",
		})
		if err != nil {
			t.Fatalf("CreatePost() error = %v", err)
		}
	})

	t.Run("accepts content at 20000 Unicode characters", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		_, err := svc.CreatePost(auth.WithAuthorID(t.Context(), "author"), CreatePostInput{
			Title:   "title",
			Content: strings.Repeat("я", 20_000),
		})
		if err != nil {
			t.Fatalf("CreatePost() error = %v", err)
		}
	})

	tests := []struct {
		name  string
		input CreatePostInput
	}{
		{name: "rejects blank title", input: CreatePostInput{Title: " \t\n", Content: "body"}},
		{name: "rejects title over 200 runes", input: CreatePostInput{Title: strings.Repeat("я", 201), Content: "body"}},
		{name: "rejects blank content", input: CreatePostInput{Title: "title", Content: " \t\n"}},
		{name: "rejects content over 20000 runes", input: CreatePostInput{Title: "title", Content: strings.Repeat("я", 20_001)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := New(memory.New(), nil, fixedClock)
			_, err := svc.CreatePost(auth.WithAuthorID(t.Context(), "author"), tt.input)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("CreatePost() error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestService_SetPostCommentsEnabled(t *testing.T) {
	t.Run("updates setting for owner", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "owner")
		post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}

		updated, err := svc.SetPostCommentsEnabled(ctx, post.ID, false)
		if err != nil {
			t.Fatalf("SetPostCommentsEnabled() error = %v", err)
		}
		if updated.CommentsEnabled {
			t.Fatal("CommentsEnabled = true, want false")
		}
	})

	t.Run("rejects non-owner", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		post, err := svc.CreatePost(auth.WithAuthorID(t.Context(), "owner"), CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}

		_, err = svc.SetPostCommentsEnabled(auth.WithAuthorID(t.Context(), "other"), post.ID, false)
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("SetPostCommentsEnabled() error = %v, want ErrForbidden", err)
		}
	})
}

func TestService_CreateComment(t *testing.T) {
	t.Run("successful save delivers to broker subscriber", func(t *testing.T) {
		t.Parallel()

		broker := subscription.New(1)
		svc := New(memory.New(), broker, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "author")
		post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}
		comments := broker.Subscribe(t.Context(), post.ID)

		want, err := svc.CreateComment(ctx, CreateCommentInput{PostID: post.ID, Text: "comment"})
		if err != nil {
			t.Fatalf("CreateComment() error = %v", err)
		}
		select {
		case got := <-comments:
			if got != want {
				t.Fatalf("received comment = %+v, want %+v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("saved comment was not delivered")
		}
	})

	t.Run("failed save does not deliver to broker subscriber", func(t *testing.T) {
		t.Parallel()

		storageErr := errors.New("storage failed")
		postID := uuid.New()
		broker := subscription.New(1)
		repo := &createCommentRepository{err: storageErr}
		svc := New(repo, broker, fixedClock)
		comments := broker.Subscribe(t.Context(), postID)

		_, err := svc.CreateComment(auth.WithAuthorID(t.Context(), "author"), CreateCommentInput{
			PostID: postID,
			Text:   "comment",
		})
		if !errors.Is(err, storageErr) {
			t.Fatalf("CreateComment() error = %v, want storage error", err)
		}
		select {
		case got := <-comments:
			t.Fatalf("received comment after failed save: %+v", got)
		default:
		}
	})

	t.Run("creates comment and publishes saved value", func(t *testing.T) {
		t.Parallel()

		publisher := &recordingPublisher{}
		svc := New(memory.New(), publisher, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "author")
		post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}

		comment, err := svc.CreateComment(ctx, CreateCommentInput{PostID: post.ID, Text: "comment"})
		if err != nil {
			t.Fatalf("CreateComment() error = %v", err)
		}
		if comment.ID == uuid.Nil || comment.AuthorID != "author" || !comment.CreatedAt.Equal(fixedTime.UTC()) {
			t.Fatalf("CreateComment() = %+v", comment)
		}
		if len(publisher.comments) != 1 || publisher.comments[0] != comment {
			t.Fatalf("published comments = %+v, want [%+v]", publisher.comments, comment)
		}
	})

	t.Run("rejects text over 2000 runes", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "author")
		post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}

		_, err = svc.CreateComment(ctx, CreateCommentInput{PostID: post.ID, Text: strings.Repeat("я", 2_001)})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("CreateComment() error = %v, want ErrValidation", err)
		}
	})

	t.Run("accepts text at 2000 Unicode characters", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "author")
		post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}

		_, err = svc.CreateComment(ctx, CreateCommentInput{PostID: post.ID, Text: strings.Repeat("я", 2_000)})
		if err != nil {
			t.Fatalf("CreateComment() error = %v", err)
		}
	})

	t.Run("does not publish when storage fails", func(t *testing.T) {
		t.Parallel()

		storageErr := errors.New("storage failed")
		repo := &createCommentRepository{err: storageErr}
		publisher := &recordingPublisher{}
		svc := New(repo, publisher, fixedClock)

		_, err := svc.CreateComment(auth.WithAuthorID(t.Context(), "author"), CreateCommentInput{
			PostID: uuid.New(),
			Text:   "comment",
		})
		if !errors.Is(err, storageErr) {
			t.Fatalf("CreateComment() error = %v, want storage error", err)
		}
		if len(publisher.comments) != 0 {
			t.Fatalf("published comments = %+v, want none", publisher.comments)
		}
	})

	t.Run("publishes value returned by storage", func(t *testing.T) {
		t.Parallel()

		persisted := domain.Comment{
			ID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			PostID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
			AuthorID:  "stored-author",
			Text:      "stored text",
			CreatedAt: fixedTime.Add(time.Hour),
		}
		repo := &createCommentRepository{result: persisted}
		publisher := &recordingPublisher{}
		svc := New(repo, publisher, fixedClock)

		comment, err := svc.CreateComment(auth.WithAuthorID(t.Context(), "author"), CreateCommentInput{
			PostID: persisted.PostID,
			Text:   "input text",
		})
		if err != nil {
			t.Fatalf("CreateComment() error = %v", err)
		}
		if comment != persisted {
			t.Fatalf("CreateComment() = %+v, want persisted value %+v", comment, persisted)
		}
		if len(publisher.comments) != 1 || publisher.comments[0] != persisted {
			t.Fatalf("published comments = %+v, want [%+v]", publisher.comments, persisted)
		}
	})

	t.Run("rejects disabled comments", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "author")
		post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.SetPostCommentsEnabled(ctx, post.ID, false); err != nil {
			t.Fatal(err)
		}

		_, err = svc.CreateComment(ctx, CreateCommentInput{PostID: post.ID, Text: "comment"})
		if !errors.Is(err, domain.ErrCommentsDisabled) {
			t.Fatalf("CreateComment() error = %v, want ErrCommentsDisabled", err)
		}
	})

	t.Run("rejects parent from another post", func(t *testing.T) {
		t.Parallel()

		svc := New(memory.New(), nil, fixedClock)
		ctx := auth.WithAuthorID(t.Context(), "author")
		firstPost, err := svc.CreatePost(ctx, CreatePostInput{Title: "first", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}
		secondPost, err := svc.CreatePost(ctx, CreatePostInput{Title: "second", Content: "body"})
		if err != nil {
			t.Fatal(err)
		}
		parent, err := svc.CreateComment(ctx, CreateCommentInput{PostID: firstPost.ID, Text: "parent"})
		if err != nil {
			t.Fatal(err)
		}

		_, err = svc.CreateComment(ctx, CreateCommentInput{PostID: secondPost.ID, ParentID: &parent.ID, Text: "reply"})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("CreateComment() error = %v, want ErrValidation", err)
		}
	})
}

func TestService_Reads(t *testing.T) {
	t.Parallel()

	svc := New(memory.New(), nil, fixedClock)
	ctx := auth.WithAuthorID(t.Context(), "author")
	post, err := svc.CreatePost(ctx, CreatePostInput{Title: "title", Content: "body"})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := svc.CreateComment(ctx, CreateCommentInput{PostID: post.ID, Text: "comment"})
	if err != nil {
		t.Fatal(err)
	}

	gotPost, err := svc.Post(t.Context(), post.ID)
	if err != nil || gotPost != post {
		t.Fatalf("Post() = %+v, %v; want %+v", gotPost, err, post)
	}
	posts, err := svc.Posts(t.Context(), pagination.PageRequest{First: 10})
	if err != nil || len(posts.Items) != 1 || posts.Items[0] != post {
		t.Fatalf("Posts() = %+v, %v", posts, err)
	}
	comments, err := svc.Comments(t.Context(), post.ID, nil, pagination.PageRequest{First: 10})
	if err != nil || len(comments.Items) != 1 || comments.Items[0] != comment {
		t.Fatalf("Comments() = %+v, %v", comments, err)
	}
}

type recordingPublisher struct {
	comments []domain.Comment
}

func (p *recordingPublisher) Publish(comment domain.Comment) {
	p.comments = append(p.comments, comment)
}

type createCommentRepository struct {
	repository.Repository
	result domain.Comment
	err    error
}

func (r *createCommentRepository) CreateComment(context.Context, domain.Comment) (domain.Comment, error) {
	return r.result, r.err
}
