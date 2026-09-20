package loader

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vikstrous/dataloadgen"

	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
)

type BatchSource interface {
	ListCommentPages(context.Context, []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error)
}

type CommentLoader = dataloadgen.Loader[repository.CommentPageKey, pagination.Page[domain.Comment]]

func NewCommentLoader(source BatchSource) *CommentLoader {
	return dataloadgen.NewMappedLoader(source.ListCommentPages, dataloadgen.WithWait(time.Millisecond))
}

type contextKey struct{}

var errMissingLoader = errors.New("comment loader is missing from request context")

func Middleware(source BatchSource, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithNewCommentLoader(r.Context(), source)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func WithNewCommentLoader(ctx context.Context, source BatchSource) context.Context {
	return context.WithValue(ctx, contextKey{}, NewCommentLoader(source))
}

func FromContext(ctx context.Context) (*CommentLoader, error) {
	commentLoader, ok := ctx.Value(contextKey{}).(*CommentLoader)
	if !ok || commentLoader == nil {
		return nil, errMissingLoader
	}
	return commentLoader, nil
}
