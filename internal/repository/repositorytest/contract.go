package repositorytest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
)

func RunContractTests(t *testing.T, factory func(t *testing.T) repository.Repository) {
	t.Helper()

	t.Run("creates and gets a post", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		want := domain.Post{
			ID:              uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			AuthorID:        "author",
			Title:           "title",
			Content:         "content",
			CommentsEnabled: true,
			CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}

		created, err := repo.CreatePost(ctx, want)
		if err != nil {
			t.Fatalf("CreatePost() error = %v", err)
		}
		if created != want {
			t.Fatalf("CreatePost() = %#v, want %#v", created, want)
		}

		got, err := repo.GetPost(ctx, want.ID)
		if err != nil {
			t.Fatalf("GetPost() error = %v", err)
		}
		if got != want {
			t.Fatalf("GetPost() = %#v, want %#v", got, want)
		}
	})

	t.Run("sorts posts by creation time and UUID descending", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		posts := []domain.Post{
			{ID: uuid.MustParse("10000000-0000-0000-0000-000000000000"), AuthorID: "a", Title: "old", Content: "old", CommentsEnabled: true, CreatedAt: base.Add(-time.Second)},
			{ID: uuid.MustParse("20000000-0000-0000-0000-000000000000"), AuthorID: "a", Title: "lower tie", Content: "tie", CommentsEnabled: true, CreatedAt: base},
			{ID: uuid.MustParse("30000000-0000-0000-0000-000000000000"), AuthorID: "a", Title: "higher tie", Content: "tie", CommentsEnabled: true, CreatedAt: base},
		}
		for _, post := range posts {
			if _, err := repo.CreatePost(ctx, post); err != nil {
				t.Fatalf("CreatePost() error = %v", err)
			}
		}

		page, err := repo.ListPosts(ctx, pagination.PageRequest{First: 3})
		if err != nil {
			t.Fatalf("ListPosts() error = %v", err)
		}
		wantIDs := []uuid.UUID{posts[2].ID, posts[1].ID, posts[0].ID}
		assertPostIDs(t, page.Items, wantIDs)
		if page.HasNextPage {
			t.Fatal("ListPosts() HasNextPage = true, want false")
		}
		if page.EndCursor == "" {
			t.Fatal("ListPosts() EndCursor is empty")
		}
	})

	t.Run("post pagination is stable across equal timestamps", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		ids := []uuid.UUID{
			uuid.MustParse("10000000-0000-0000-0000-000000000000"),
			uuid.MustParse("20000000-0000-0000-0000-000000000000"),
			uuid.MustParse("30000000-0000-0000-0000-000000000000"),
		}
		for _, id := range ids {
			_, err := repo.CreatePost(ctx, domain.Post{
				ID: id, AuthorID: "author", Title: "title", Content: "content",
				CommentsEnabled: true, CreatedAt: base,
			})
			if err != nil {
				t.Fatalf("CreatePost() error = %v", err)
			}
		}

		first, err := repo.ListPosts(ctx, pagination.PageRequest{First: 2})
		if err != nil {
			t.Fatalf("first ListPosts() error = %v", err)
		}
		if !first.HasNextPage || len(first.Items) != 2 || first.EndCursor == "" {
			t.Fatalf("first page = %#v", first)
		}

		second, err := repo.ListPosts(ctx, pagination.PageRequest{First: 2, After: first.EndCursor})
		if err != nil {
			t.Fatalf("second ListPosts() error = %v", err)
		}
		if second.HasNextPage || len(second.Items) != 1 {
			t.Fatalf("second page = %#v", second)
		}
		assertPostIDs(t, append(first.Items, second.Items...), []uuid.UUID{ids[2], ids[1], ids[0]})
	})

	t.Run("post pagination continues across distinct timestamps", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		posts := []domain.Post{
			{ID: uuid.MustParse("10000000-0000-0000-0000-000000000000"), AuthorID: "author", Title: "old", Content: "content", CommentsEnabled: true, CreatedAt: base},
			{ID: uuid.MustParse("20000000-0000-0000-0000-000000000000"), AuthorID: "author", Title: "middle", Content: "content", CommentsEnabled: true, CreatedAt: base.Add(time.Second)},
			{ID: uuid.MustParse("30000000-0000-0000-0000-000000000000"), AuthorID: "author", Title: "new", Content: "content", CommentsEnabled: true, CreatedAt: base.Add(2 * time.Second)},
		}
		for _, post := range posts {
			if _, err := repo.CreatePost(ctx, post); err != nil {
				t.Fatalf("CreatePost() error = %v", err)
			}
		}

		first, err := repo.ListPosts(ctx, pagination.PageRequest{First: 2})
		if err != nil {
			t.Fatalf("first ListPosts() error = %v", err)
		}
		if !first.HasNextPage || len(first.Items) != 2 || first.EndCursor == "" {
			t.Fatalf("first page = %#v", first)
		}
		second, err := repo.ListPosts(ctx, pagination.PageRequest{First: 2, After: first.EndCursor})
		if err != nil {
			t.Fatalf("second ListPosts() error = %v", err)
		}
		if second.HasNextPage || len(second.Items) != 1 {
			t.Fatalf("second page = %#v", second)
		}
		assertPostIDs(t, append(first.Items, second.Items...), []uuid.UUID{posts[2].ID, posts[1].ID, posts[0].ID})
	})

	t.Run("sets comments enabled", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := domain.Post{ID: uuid.New(), AuthorID: "author", Title: "title", Content: "content", CommentsEnabled: true, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		if _, err := repo.CreatePost(ctx, post); err != nil {
			t.Fatalf("CreatePost() error = %v", err)
		}

		updated, err := repo.SetCommentsEnabled(ctx, post.ID, false)
		if err != nil {
			t.Fatalf("SetCommentsEnabled() error = %v", err)
		}
		if updated.CommentsEnabled {
			t.Fatal("SetCommentsEnabled() left comments enabled")
		}

		got, err := repo.GetPost(ctx, post.ID)
		if err != nil {
			t.Fatalf("GetPost() error = %v", err)
		}
		if got.CommentsEnabled {
			t.Fatal("GetPost() returned comments enabled after update")
		}
	})

	t.Run("lists only root comments", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, true)
		otherPost := createPost(t, repo, true)
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		older := createComment(t, repo, domain.Comment{ID: uuid.MustParse("10000000-0000-0000-0000-000000000000"), PostID: post.ID, AuthorID: "a", Text: "older", CreatedAt: base})
		newer := createComment(t, repo, domain.Comment{ID: uuid.MustParse("20000000-0000-0000-0000-000000000000"), PostID: post.ID, AuthorID: "a", Text: "newer", CreatedAt: base.Add(time.Second)})
		createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, ParentID: &older.ID, AuthorID: "a", Text: "reply", CreatedAt: base.Add(2 * time.Second)})
		createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: otherPost.ID, AuthorID: "a", Text: "other post", CreatedAt: base.Add(3 * time.Second)})

		page, err := repo.ListComments(ctx, post.ID, nil, pagination.PageRequest{First: 10})
		if err != nil {
			t.Fatalf("ListComments() error = %v", err)
		}
		assertCommentIDs(t, page.Items, []uuid.UUID{newer.ID, older.ID})
		if page.HasNextPage {
			t.Fatal("ListComments() HasNextPage = true, want false")
		}
	})

	t.Run("reply pagination is stable across equal timestamps", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, true)
		parent := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "a", Text: "parent", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		otherParent := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "a", Text: "other parent", CreatedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)})
		base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		replies := []domain.Comment{
			{ID: uuid.MustParse("10000000-0000-0000-0000-000000000000"), PostID: post.ID, ParentID: &parent.ID, AuthorID: "a", Text: "old", CreatedAt: base},
			{ID: uuid.MustParse("20000000-0000-0000-0000-000000000000"), PostID: post.ID, ParentID: &parent.ID, AuthorID: "a", Text: "middle", CreatedAt: base},
			{ID: uuid.MustParse("30000000-0000-0000-0000-000000000000"), PostID: post.ID, ParentID: &parent.ID, AuthorID: "a", Text: "new", CreatedAt: base},
		}
		for _, reply := range replies {
			createComment(t, repo, reply)
		}
		createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, ParentID: &otherParent.ID, AuthorID: "a", Text: "different thread", CreatedAt: base.Add(3 * time.Second)})

		first, err := repo.ListComments(ctx, post.ID, &parent.ID, pagination.PageRequest{First: 2})
		if err != nil {
			t.Fatalf("first ListComments() error = %v", err)
		}
		if !first.HasNextPage || len(first.Items) != 2 || first.EndCursor == "" {
			t.Fatalf("first page = %#v", first)
		}
		second, err := repo.ListComments(ctx, post.ID, &parent.ID, pagination.PageRequest{First: 2, After: first.EndCursor})
		if err != nil {
			t.Fatalf("second ListComments() error = %v", err)
		}
		if second.HasNextPage || len(second.Items) != 1 {
			t.Fatalf("second page = %#v", second)
		}
		assertCommentIDs(t, append(first.Items, second.Items...), []uuid.UUID{replies[2].ID, replies[1].ID, replies[0].ID})
	})

	t.Run("reply pagination continues across distinct timestamps", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, true)
		parent := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "a", Text: "parent", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		replies := []domain.Comment{
			{ID: uuid.MustParse("10000000-0000-0000-0000-000000000000"), PostID: post.ID, ParentID: &parent.ID, AuthorID: "a", Text: "old", CreatedAt: base},
			{ID: uuid.MustParse("20000000-0000-0000-0000-000000000000"), PostID: post.ID, ParentID: &parent.ID, AuthorID: "a", Text: "middle", CreatedAt: base.Add(time.Second)},
			{ID: uuid.MustParse("30000000-0000-0000-0000-000000000000"), PostID: post.ID, ParentID: &parent.ID, AuthorID: "a", Text: "new", CreatedAt: base.Add(2 * time.Second)},
		}
		for _, reply := range replies {
			createComment(t, repo, reply)
		}

		first, err := repo.ListComments(ctx, post.ID, &parent.ID, pagination.PageRequest{First: 2})
		if err != nil {
			t.Fatalf("first ListComments() error = %v", err)
		}
		if !first.HasNextPage || len(first.Items) != 2 || first.EndCursor == "" {
			t.Fatalf("first page = %#v", first)
		}
		second, err := repo.ListComments(ctx, post.ID, &parent.ID, pagination.PageRequest{First: 2, After: first.EndCursor})
		if err != nil {
			t.Fatalf("second ListComments() error = %v", err)
		}
		if second.HasNextPage || len(second.Items) != 1 {
			t.Fatalf("second page = %#v", second)
		}
		assertCommentIDs(t, append(first.Items, second.Items...), []uuid.UUID{replies[2].ID, replies[1].ID, replies[0].ID})
	})

	t.Run("returns not found errors", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		missing := uuid.New()

		if _, err := repo.GetPost(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("GetPost() error = %v, want ErrNotFound", err)
		}
		if _, err := repo.SetCommentsEnabled(ctx, missing, false); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("SetCommentsEnabled() error = %v, want ErrNotFound", err)
		}
		if _, err := repo.GetComment(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("GetComment() error = %v, want ErrNotFound", err)
		}
		if _, err := repo.ListComments(ctx, missing, nil, pagination.PageRequest{First: 10}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("ListComments() error = %v, want ErrNotFound", err)
		}
		comment := domain.Comment{ID: uuid.New(), PostID: missing, AuthorID: "a", Text: "missing post", CreatedAt: time.Now().UTC()}
		if _, err := repo.CreateComment(ctx, comment); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("CreateComment() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("rejects comments when disabled", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, false)
		comment := domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "a", Text: "comment", CreatedAt: time.Now().UTC()}

		if _, err := repo.CreateComment(ctx, comment); !errors.Is(err, domain.ErrCommentsDisabled) {
			t.Fatalf("CreateComment() error = %v, want ErrCommentsDisabled", err)
		}
	})

	t.Run("rejects a missing parent", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, true)
		missingParent := uuid.New()
		comment := domain.Comment{ID: uuid.New(), PostID: post.ID, ParentID: &missingParent, AuthorID: "a", Text: "reply", CreatedAt: time.Now().UTC()}

		if _, err := repo.CreateComment(ctx, comment); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("CreateComment() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("rejects parent from another post", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		firstPost := createPost(t, repo, true)
		secondPost := createPost(t, repo, true)
		parent := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: firstPost.ID, AuthorID: "a", Text: "parent", CreatedAt: time.Now().UTC()})

		_, err := repo.CreateComment(ctx, domain.Comment{ID: uuid.New(), PostID: secondPost.ID, ParentID: &parent.ID, AuthorID: "a", Text: "reply", CreatedAt: time.Now().UTC()})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("CreateComment() error = %v, want ErrValidation", err)
		}
	})

	t.Run("rejects invalid page requests", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, true)

		if _, err := repo.ListPosts(ctx, pagination.PageRequest{First: 0}); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("ListPosts() error = %v, want ErrValidation", err)
		}
		if _, err := repo.ListPosts(ctx, pagination.PageRequest{First: 10, After: "invalid"}); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("ListPosts() cursor error = %v, want ErrValidation", err)
		}
		if _, err := repo.ListComments(ctx, post.ID, nil, pagination.PageRequest{First: 101}); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("ListComments() error = %v, want ErrValidation", err)
		}
	})

	t.Run("batches root and reply comment pages", func(t *testing.T) {
		repo := factory(t)
		ctx := context.Background()
		post := createPost(t, repo, true)
		otherPost := createPost(t, repo, true)
		emptyPost := createPost(t, repo, true)
		root := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "author", Text: "root", CreatedAt: time.Now().UTC()})
		olderRoot := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "author", Text: "older root", CreatedAt: root.CreatedAt.Add(-time.Second)})
		reply := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, ParentID: &root.ID, AuthorID: "author", Text: "reply", CreatedAt: time.Now().UTC()})
		otherRoot := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: otherPost.ID, AuthorID: "author", Text: "other root", CreatedAt: time.Now().UTC()})
		rootKey := repository.CommentPageKey{PostID: post.ID, Root: true, First: 1}
		replyKey := repository.CommentPageKey{PostID: post.ID, ParentID: root.ID, First: 10}
		otherRootKey := repository.CommentPageKey{PostID: otherPost.ID, Root: true, First: 10}
		emptyRootKey := repository.CommentPageKey{PostID: emptyPost.ID, Root: true, First: 10}

		pages, err := repo.ListCommentPages(ctx, []repository.CommentPageKey{rootKey, replyKey, otherRootKey, emptyRootKey})
		if err != nil {
			t.Fatalf("ListCommentPages() error = %v", err)
		}
		if got := pages[rootKey]; len(got.Items) != 1 || got.Items[0].ID != root.ID || !got.HasNextPage || got.EndCursor == "" {
			t.Fatalf("root page = %#v", pages[rootKey])
		}
		nextRootKey := repository.CommentPageKey{PostID: post.ID, Root: true, First: 1, After: pages[rootKey].EndCursor}
		nextPages, err := repo.ListCommentPages(ctx, []repository.CommentPageKey{nextRootKey})
		if err != nil {
			t.Fatalf("ListCommentPages(next page) error = %v", err)
		}
		if got := nextPages[nextRootKey].Items; len(got) != 1 || got[0].ID != olderRoot.ID {
			t.Fatalf("next root page = %#v", nextPages[nextRootKey])
		}
		if got := pages[replyKey].Items; len(got) != 1 || got[0].ID != reply.ID {
			t.Fatalf("reply page = %#v", pages[replyKey])
		}
		if got := pages[otherRootKey].Items; len(got) != 1 || got[0].ID != otherRoot.ID {
			t.Fatalf("other root page = %#v", pages[otherRootKey])
		}
		if page, ok := pages[emptyRootKey]; !ok || len(page.Items) != 0 {
			t.Fatalf("empty root page = %#v, present = %v", page, ok)
		}
	})

	t.Run("batch comment pages preserve validation and not found errors", func(t *testing.T) {
		repo := factory(t)
		post := createPost(t, repo, true)
		cases := []struct {
			name string
			key  repository.CommentPageKey
			want error
		}{
			{name: "missing post", key: repository.CommentPageKey{PostID: uuid.New(), Root: true, First: 10}, want: domain.ErrNotFound},
			{name: "root with parent", key: repository.CommentPageKey{PostID: post.ID, ParentID: uuid.New(), Root: true, First: 10}, want: domain.ErrValidation},
			{name: "reply without parent", key: repository.CommentPageKey{PostID: post.ID, First: 10}, want: domain.ErrValidation},
			{name: "invalid page size", key: repository.CommentPageKey{PostID: post.ID, Root: true, First: 0}, want: domain.ErrValidation},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := repo.ListCommentPages(t.Context(), []repository.CommentPageKey{tc.key})
				if !errors.Is(err, tc.want) {
					t.Fatalf("ListCommentPages() error = %v, want %v", err, tc.want)
				}
			})
		}
	})

	t.Run("duplicate batch keys do not duplicate comments", func(t *testing.T) {
		repo := factory(t)
		post := createPost(t, repo, true)
		root := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, AuthorID: "author", Text: "root", CreatedAt: time.Now().UTC()})
		reply := createComment(t, repo, domain.Comment{ID: uuid.New(), PostID: post.ID, ParentID: &root.ID, AuthorID: "author", Text: "reply", CreatedAt: time.Now().UTC()})
		rootKey := repository.CommentPageKey{PostID: post.ID, Root: true, First: 1}
		replyKey := repository.CommentPageKey{PostID: post.ID, ParentID: root.ID, First: 1}

		pages, err := repo.ListCommentPages(t.Context(), []repository.CommentPageKey{rootKey, rootKey, replyKey, replyKey})
		if err != nil {
			t.Fatalf("ListCommentPages() error = %v", err)
		}
		if got := pages[rootKey]; len(got.Items) != 1 || got.Items[0].ID != root.ID || got.HasNextPage {
			t.Fatalf("duplicate root key page = %#v", got)
		}
		if got := pages[replyKey]; len(got.Items) != 1 || got.Items[0].ID != reply.ID || got.HasNextPage {
			t.Fatalf("duplicate reply key page = %#v", got)
		}
	})
}

func createPost(t *testing.T, repo repository.Repository, commentsEnabled bool) domain.Post {
	t.Helper()
	post := domain.Post{
		ID:              uuid.New(),
		AuthorID:        "author",
		Title:           "title",
		Content:         "content",
		CommentsEnabled: commentsEnabled,
		CreatedAt:       time.Now().UTC(),
	}
	created, err := repo.CreatePost(context.Background(), post)
	if err != nil {
		t.Fatalf("CreatePost() error = %v", err)
	}
	return created
}

func createComment(t *testing.T, repo repository.Repository, comment domain.Comment) domain.Comment {
	t.Helper()
	created, err := repo.CreateComment(context.Background(), comment)
	if err != nil {
		t.Fatalf("CreateComment() error = %v", err)
	}
	return created
}

func assertPostIDs(t *testing.T, posts []domain.Post, want []uuid.UUID) {
	t.Helper()
	if len(posts) != len(want) {
		t.Fatalf("post count = %d, want %d", len(posts), len(want))
	}
	for i := range want {
		if posts[i].ID != want[i] {
			t.Fatalf("post[%d].ID = %s, want %s", i, posts[i].ID, want[i])
		}
	}
}

func assertCommentIDs(t *testing.T, comments []domain.Comment, want []uuid.UUID) {
	t.Helper()
	if len(comments) != len(want) {
		t.Fatalf("comment count = %d, want %d", len(comments), len(want))
	}
	for i := range want {
		if comments[i].ID != want[i] {
			t.Fatalf("comment[%d].ID = %s, want %s", i, comments[i].ID, want[i])
		}
	}
}
