package graphqlapi

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"testozon/internal/domain"
)

func ErrorPresenter(ctx context.Context, err error) *gqlerror.Error {
	presented := graphql.DefaultErrorPresenter(ctx, err)

	for sentinel, code := range map[error]string{
		domain.ErrUnauthenticated:  "UNAUTHENTICATED",
		domain.ErrForbidden:        "FORBIDDEN",
		domain.ErrNotFound:         "NOT_FOUND",
		domain.ErrCommentsDisabled: "COMMENTS_DISABLED",
		domain.ErrValidation:       "VALIDATION_FAILED",
	} {
		if errors.Is(err, sentinel) {
			presented.Message = sentinel.Error()
			presented.Extensions = map[string]any{"code": code}
			return presented
		}
	}

	var graphError *gqlerror.Error
	if errors.As(err, &graphError) {
		code, _ := graphError.Extensions["code"].(string)
		if code == "GRAPHQL_PARSE_FAILED" || code == "GRAPHQL_VALIDATION_FAILED" {
			return presented
		}
	}

	presented.Message = "internal error"
	presented.Extensions = map[string]any{"code": "INTERNAL"}
	return presented
}
