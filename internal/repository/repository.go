package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"testozon/internal/domain"
	"testozon/internal/pagination"
)

type Repository interface {
	CreatePost(context.Context, domain.Post) (domain.Post, error)
	GetPost(context.Context, uuid.UUID) (domain.Post, error)
	ListPosts(context.Context, pagination.PageRequest) (pagination.Page[domain.Post], error)
	SetCommentsEnabled(context.Context, uuid.UUID, bool) (domain.Post, error)
	CreateComment(context.Context, domain.Comment) (domain.Comment, error)
	GetComment(context.Context, uuid.UUID) (domain.Comment, error)
	ListComments(context.Context, uuid.UUID, *uuid.UUID, pagination.PageRequest) (pagination.Page[domain.Comment], error)
	ListCommentPages(context.Context, []CommentPageKey) (map[CommentPageKey]pagination.Page[domain.Comment], error)
}

type CommentPageKey struct {
	PostID   uuid.UUID
	ParentID uuid.UUID
	Root     bool
	First    int
	After    string
}

func (k CommentPageKey) Validate() error {
	if err := pagination.ValidateFirst(k.First); err != nil {
		return err
	}
	if k.After != "" {
		if _, _, err := pagination.Decode(k.After); err != nil {
			return err
		}
	}
	if k.Root && k.ParentID != uuid.Nil {
		return fmt.Errorf("%w: root comment page cannot have a parent", domain.ErrValidation)
	}
	if !k.Root && k.ParentID == uuid.Nil {
		return fmt.Errorf("%w: reply comment page requires a parent", domain.ErrValidation)
	}
	return nil
}

func (k CommentPageKey) PageRequest() pagination.PageRequest {
	return pagination.PageRequest{First: k.First, After: k.After}
}
