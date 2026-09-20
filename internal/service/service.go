package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"testozon/internal/auth"
	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
)

type CommentPublisher interface {
	Publish(domain.Comment)
}

type CreatePostInput struct {
	Title   string
	Content string
}

type CreateCommentInput struct {
	PostID   uuid.UUID
	ParentID *uuid.UUID
	Text     string
}

type Service struct {
	repository repository.Repository
	publisher  CommentPublisher
	now        func() time.Time
}

func New(repository repository.Repository, publisher CommentPublisher, now func() time.Time) *Service {
	return &Service{repository: repository, publisher: publisher, now: now}
}

func (s *Service) CreatePost(ctx context.Context, input CreatePostInput) (domain.Post, error) {
	authorID, err := auth.AuthorID(ctx)
	if err != nil {
		return domain.Post{}, err
	}
	if err := validateText("title", input.Title, 200); err != nil {
		return domain.Post{}, err
	}
	if err := validateText("content", input.Content, 20_000); err != nil {
		return domain.Post{}, err
	}

	post := domain.Post{
		ID:              uuid.New(),
		AuthorID:        authorID,
		Title:           input.Title,
		Content:         input.Content,
		CommentsEnabled: true,
		CreatedAt:       s.now().UTC(),
	}
	return s.repository.CreatePost(ctx, post)
}

func (s *Service) SetPostCommentsEnabled(ctx context.Context, postID uuid.UUID, enabled bool) (domain.Post, error) {
	authorID, err := auth.AuthorID(ctx)
	if err != nil {
		return domain.Post{}, err
	}
	post, err := s.repository.GetPost(ctx, postID)
	if err != nil {
		return domain.Post{}, err
	}
	if post.AuthorID != authorID {
		return domain.Post{}, fmt.Errorf("set comments for post %s: %w", postID, domain.ErrForbidden)
	}
	return s.repository.SetCommentsEnabled(ctx, postID, enabled)
}

func (s *Service) CreateComment(ctx context.Context, input CreateCommentInput) (domain.Comment, error) {
	authorID, err := auth.AuthorID(ctx)
	if err != nil {
		return domain.Comment{}, err
	}
	if err := validateText("comment text", input.Text, 2_000); err != nil {
		return domain.Comment{}, err
	}
	if input.ParentID != nil {
		parent, err := s.repository.GetComment(ctx, *input.ParentID)
		if err != nil {
			return domain.Comment{}, err
		}
		if parent.PostID != input.PostID {
			return domain.Comment{}, fmt.Errorf("create reply: %w: parent comment belongs to another post", domain.ErrValidation)
		}
	}

	comment := domain.Comment{
		ID:        uuid.New(),
		PostID:    input.PostID,
		ParentID:  input.ParentID,
		AuthorID:  authorID,
		Text:      input.Text,
		CreatedAt: s.now().UTC(),
	}
	comment, err = s.repository.CreateComment(ctx, comment)
	if err != nil {
		return domain.Comment{}, err
	}
	if s.publisher != nil {
		s.publisher.Publish(comment)
	}
	return comment, nil
}

func (s *Service) Posts(ctx context.Context, request pagination.PageRequest) (pagination.Page[domain.Post], error) {
	return s.repository.ListPosts(ctx, request)
}

func (s *Service) Post(ctx context.Context, postID uuid.UUID) (domain.Post, error) {
	return s.repository.GetPost(ctx, postID)
}

func (s *Service) Comments(ctx context.Context, postID uuid.UUID, parentID *uuid.UUID, request pagination.PageRequest) (pagination.Page[domain.Comment], error) {
	return s.repository.ListComments(ctx, postID, parentID, request)
}

func validateText(field, value string, maximum int) error {
	length := utf8.RuneCountInString(value)
	if strings.TrimSpace(value) == "" || length > maximum {
		return fmt.Errorf("%w: %s must contain between 1 and %d characters", domain.ErrValidation, field, maximum)
	}
	return nil
}
