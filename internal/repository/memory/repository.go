package memory

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
)

type Repository struct {
	mu       sync.RWMutex
	posts    map[uuid.UUID]domain.Post
	comments map[uuid.UUID]domain.Comment
}

func New() *Repository {
	return &Repository{
		posts:    make(map[uuid.UUID]domain.Post),
		comments: make(map[uuid.UUID]domain.Comment),
	}
}

func (r *Repository) CreatePost(_ context.Context, post domain.Post) (domain.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.posts[post.ID] = post
	return post, nil
}

func (r *Repository) GetPost(_ context.Context, id uuid.UUID) (domain.Post, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	post, ok := r.posts[id]
	if !ok {
		return domain.Post{}, fmt.Errorf("get post %s: %w", id, domain.ErrNotFound)
	}
	return post, nil
}

func (r *Repository) ListPosts(_ context.Context, request pagination.PageRequest) (pagination.Page[domain.Post], error) {
	afterTime, afterID, hasAfter, err := validatePageRequest(request)
	if err != nil {
		return pagination.Page[domain.Post]{}, err
	}

	r.mu.RLock()
	posts := make([]domain.Post, 0, len(r.posts))
	for _, post := range r.posts {
		posts = append(posts, post)
	}
	r.mu.RUnlock()

	sort.Slice(posts, func(i, j int) bool {
		return comesBefore(posts[i].CreatedAt, posts[i].ID, posts[j].CreatedAt, posts[j].ID)
	})
	if hasAfter {
		posts = filterPostsAfter(posts, afterTime, afterID)
	}

	return postPage(posts, request.First), nil
}

func (r *Repository) SetCommentsEnabled(_ context.Context, id uuid.UUID, enabled bool) (domain.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	post, ok := r.posts[id]
	if !ok {
		return domain.Post{}, fmt.Errorf("set comments enabled for post %s: %w", id, domain.ErrNotFound)
	}
	post.CommentsEnabled = enabled
	r.posts[id] = post
	return post, nil
}

func (r *Repository) CreateComment(_ context.Context, comment domain.Comment) (domain.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	post, ok := r.posts[comment.PostID]
	if !ok {
		return domain.Comment{}, fmt.Errorf("create comment for post %s: %w", comment.PostID, domain.ErrNotFound)
	}
	if !post.CommentsEnabled {
		return domain.Comment{}, fmt.Errorf("create comment for post %s: %w", comment.PostID, domain.ErrCommentsDisabled)
	}
	if comment.ParentID != nil {
		parent, ok := r.comments[*comment.ParentID]
		if !ok {
			return domain.Comment{}, fmt.Errorf("create reply to comment %s: %w", *comment.ParentID, domain.ErrNotFound)
		}
		if parent.PostID != comment.PostID {
			return domain.Comment{}, fmt.Errorf("%w: parent comment belongs to another post", domain.ErrValidation)
		}
	}

	comment = cloneComment(comment)
	r.comments[comment.ID] = comment
	return cloneComment(comment), nil
}

func (r *Repository) GetComment(_ context.Context, id uuid.UUID) (domain.Comment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	comment, ok := r.comments[id]
	if !ok {
		return domain.Comment{}, fmt.Errorf("get comment %s: %w", id, domain.ErrNotFound)
	}
	return cloneComment(comment), nil
}

func (r *Repository) ListComments(_ context.Context, postID uuid.UUID, parentID *uuid.UUID, request pagination.PageRequest) (pagination.Page[domain.Comment], error) {
	afterTime, afterID, hasAfter, err := validatePageRequest(request)
	if err != nil {
		return pagination.Page[domain.Comment]{}, err
	}

	r.mu.RLock()
	if _, ok := r.posts[postID]; !ok {
		r.mu.RUnlock()
		return pagination.Page[domain.Comment]{}, fmt.Errorf("list comments for post %s: %w", postID, domain.ErrNotFound)
	}
	comments := make([]domain.Comment, 0)
	for _, comment := range r.comments {
		if comment.PostID == postID && sameParent(comment.ParentID, parentID) {
			comments = append(comments, cloneComment(comment))
		}
	}
	r.mu.RUnlock()

	sort.Slice(comments, func(i, j int) bool {
		return comesBefore(comments[i].CreatedAt, comments[i].ID, comments[j].CreatedAt, comments[j].ID)
	})
	if hasAfter {
		comments = filterCommentsAfter(comments, afterTime, afterID)
	}

	return commentPage(comments, request.First), nil
}

func (r *Repository) ListCommentPages(ctx context.Context, keys []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
	for _, key := range keys {
		if err := key.Validate(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	for _, key := range keys {
		if _, ok := r.posts[key.PostID]; !ok {
			r.mu.RUnlock()
			return nil, fmt.Errorf("list comments for post %s: %w", key.PostID, domain.ErrNotFound)
		}
	}
	comments := make([]domain.Comment, 0, len(r.comments))
	for _, comment := range r.comments {
		comments = append(comments, cloneComment(comment))
	}
	r.mu.RUnlock()

	pages := make(map[repository.CommentPageKey]pagination.Page[domain.Comment], len(keys))
	for _, key := range keys {
		afterTime, afterID, hasAfter, err := validatePageRequest(key.PageRequest())
		if err != nil {
			return nil, err
		}
		items := make([]domain.Comment, 0)
		for _, comment := range comments {
			matchesParent := key.Root && comment.ParentID == nil || !key.Root && comment.ParentID != nil && *comment.ParentID == key.ParentID
			if comment.PostID == key.PostID && matchesParent {
				items = append(items, cloneComment(comment))
			}
		}
		sort.Slice(items, func(i, j int) bool {
			return comesBefore(items[i].CreatedAt, items[i].ID, items[j].CreatedAt, items[j].ID)
		})
		if hasAfter {
			items = filterCommentsAfter(items, afterTime, afterID)
		}
		pages[key] = commentPage(items, key.First)
	}
	return pages, nil
}

func validatePageRequest(request pagination.PageRequest) (time.Time, uuid.UUID, bool, error) {
	if err := pagination.ValidateFirst(request.First); err != nil {
		return time.Time{}, uuid.Nil, false, err
	}
	if request.After == "" {
		return time.Time{}, uuid.Nil, false, nil
	}

	afterTime, afterID, err := pagination.Decode(request.After)
	if err != nil {
		return time.Time{}, uuid.Nil, false, err
	}
	return afterTime, afterID, true, nil
}

func comesBefore(leftTime time.Time, leftID uuid.UUID, rightTime time.Time, rightID uuid.UUID) bool {
	if !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	return bytes.Compare(leftID[:], rightID[:]) > 0
}

func isAfterCursor(createdAt time.Time, id uuid.UUID, cursorTime time.Time, cursorID uuid.UUID) bool {
	if !createdAt.Equal(cursorTime) {
		return createdAt.Before(cursorTime)
	}
	return bytes.Compare(id[:], cursorID[:]) < 0
}

func filterPostsAfter(posts []domain.Post, afterTime time.Time, afterID uuid.UUID) []domain.Post {
	filtered := posts[:0]
	for _, post := range posts {
		if isAfterCursor(post.CreatedAt, post.ID, afterTime, afterID) {
			filtered = append(filtered, post)
		}
	}
	return filtered
}

func filterCommentsAfter(comments []domain.Comment, afterTime time.Time, afterID uuid.UUID) []domain.Comment {
	filtered := comments[:0]
	for _, comment := range comments {
		if isAfterCursor(comment.CreatedAt, comment.ID, afterTime, afterID) {
			filtered = append(filtered, comment)
		}
	}
	return filtered
}

func postPage(posts []domain.Post, first int) pagination.Page[domain.Post] {
	hasNextPage := len(posts) > first
	if hasNextPage {
		posts = posts[:first]
	}

	page := pagination.Page[domain.Post]{Items: posts, HasNextPage: hasNextPage}
	if len(posts) > 0 {
		last := posts[len(posts)-1]
		page.EndCursor = pagination.Encode(last.CreatedAt, last.ID)
	}
	return page
}

func commentPage(comments []domain.Comment, first int) pagination.Page[domain.Comment] {
	hasNextPage := len(comments) > first
	if hasNextPage {
		comments = comments[:first]
	}

	page := pagination.Page[domain.Comment]{Items: comments, HasNextPage: hasNextPage}
	if len(comments) > 0 {
		last := comments[len(comments)-1]
		page.EndCursor = pagination.Encode(last.CreatedAt, last.ID)
	}
	return page
}

func sameParent(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneComment(comment domain.Comment) domain.Comment {
	if comment.ParentID != nil {
		parentID := *comment.ParentID
		comment.ParentID = &parentID
	}
	return comment
}
