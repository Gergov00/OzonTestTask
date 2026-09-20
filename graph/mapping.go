package graph

import (
	"fmt"

	"github.com/google/uuid"

	"testozon/graph/model"
	"testozon/internal/domain"
	"testozon/internal/pagination"
)

func parseID(field, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: invalid %s", domain.ErrValidation, field)
	}
	return id, nil
}

func parseOptionalID(field string, value *string) (*uuid.UUID, error) {
	if value == nil {
		return nil, nil
	}
	id, err := parseID(field, *value)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func pageRequest(first int, after *string) pagination.PageRequest {
	request := pagination.PageRequest{First: first}
	if after != nil {
		request.After = *after
	}
	return request
}

func toPost(post domain.Post) *model.Post {
	return &model.Post{
		ID:              post.ID.String(),
		AuthorID:        post.AuthorID,
		Title:           post.Title,
		Content:         post.Content,
		CommentsEnabled: post.CommentsEnabled,
		CreatedAt:       post.CreatedAt,
	}
}

func toComment(comment domain.Comment) *model.Comment {
	var parentID *string
	if comment.ParentID != nil {
		value := comment.ParentID.String()
		parentID = &value
	}
	return &model.Comment{
		ID:        comment.ID.String(),
		PostID:    comment.PostID.String(),
		ParentID:  parentID,
		AuthorID:  comment.AuthorID,
		Text:      comment.Text,
		CreatedAt: comment.CreatedAt,
	}
}

func toPostConnection(page pagination.Page[domain.Post]) *model.PostConnection {
	nodes := make([]*model.Post, len(page.Items))
	for index, post := range page.Items {
		nodes[index] = toPost(post)
	}
	return &model.PostConnection{Nodes: nodes, PageInfo: toPageInfo(page.HasNextPage, page.EndCursor)}
}

func toCommentConnection(page pagination.Page[domain.Comment]) *model.CommentConnection {
	nodes := make([]*model.Comment, len(page.Items))
	for index, comment := range page.Items {
		nodes[index] = toComment(comment)
	}
	return &model.CommentConnection{Nodes: nodes, PageInfo: toPageInfo(page.HasNextPage, page.EndCursor)}
}

func toPageInfo(hasNextPage bool, endCursor string) *model.PageInfo {
	var cursor *string
	if endCursor != "" {
		cursor = &endCursor
	}
	return &model.PageInfo{HasNextPage: hasNextPage, EndCursor: cursor}
}
