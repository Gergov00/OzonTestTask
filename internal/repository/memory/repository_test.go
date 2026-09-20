package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
	"testozon/internal/repository/repositorytest"
)

func TestRepositoryContract(t *testing.T) {
	repositorytest.RunContractTests(t, func(t *testing.T) repository.Repository {
		t.Helper()
		return New()
	})
}

func TestRepositoryConcurrentAccess(t *testing.T) {
	repo := New()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	post := domain.Post{ID: uuid.New(), AuthorID: "author", Title: "title", Content: "content", CommentsEnabled: true, CreatedAt: base}
	if _, err := repo.CreatePost(ctx, post); err != nil {
		t.Fatalf("CreatePost() error = %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			comment := domain.Comment{
				ID:        uuid.New(),
				PostID:    post.ID,
				AuthorID:  fmt.Sprintf("author-%d", i),
				Text:      "comment",
				CreatedAt: base.Add(time.Duration(i) * time.Second),
			}
			if _, err := repo.CreateComment(ctx, comment); err != nil {
				t.Errorf("CreateComment() error = %v", err)
			}
			if _, err := repo.GetPost(ctx, post.ID); err != nil {
				t.Errorf("GetPost() error = %v", err)
			}
			if _, err := repo.ListPosts(ctx, pagination.PageRequest{First: 10}); err != nil {
				t.Errorf("ListPosts() error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	page, err := repo.ListComments(ctx, post.ID, nil, pagination.PageRequest{First: workers})
	if err != nil {
		t.Fatalf("ListComments() error = %v", err)
	}
	if len(page.Items) != workers {
		t.Fatalf("comment count = %d, want %d", len(page.Items), workers)
	}
}

func TestRepositoryCopiesCommentParentIDs(t *testing.T) {
	repo := New()
	ctx := context.Background()
	post := domain.Post{ID: uuid.New(), AuthorID: "author", Title: "title", Content: "content", CommentsEnabled: true, CreatedAt: time.Now().UTC()}
	if _, err := repo.CreatePost(ctx, post); err != nil {
		t.Fatalf("CreatePost() error = %v", err)
	}
	parent := domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "author", Text: "parent", CreatedAt: time.Now().UTC()}
	if _, err := repo.CreateComment(ctx, parent); err != nil {
		t.Fatalf("CreateComment(parent) error = %v", err)
	}

	parentID := parent.ID
	reply := domain.Comment{ID: uuid.New(), PostID: post.ID, ParentID: &parentID, AuthorID: "author", Text: "reply", CreatedAt: time.Now().UTC()}
	created, err := repo.CreateComment(ctx, reply)
	if err != nil {
		t.Fatalf("CreateComment(reply) error = %v", err)
	}
	parentID = uuid.New()
	*created.ParentID = uuid.New()

	got, err := repo.GetComment(ctx, reply.ID)
	if err != nil {
		t.Fatalf("GetComment() error = %v", err)
	}
	if got.ParentID == nil || *got.ParentID != parent.ID {
		t.Fatalf("GetComment().ParentID = %v, want %s", got.ParentID, parent.ID)
	}
}
