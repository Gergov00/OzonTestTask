package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"testozon/internal/domain"
)

type authorIDKey struct{}

func WithAuthorID(ctx context.Context, authorID string) context.Context {
	return context.WithValue(ctx, authorIDKey{}, strings.TrimSpace(authorID))
}

func AuthorID(ctx context.Context) (string, error) {
	authorID, _ := ctx.Value(authorIDKey{}).(string)
	if authorID == "" {
		return "", fmt.Errorf("get author ID: %w", domain.ErrUnauthenticated)
	}
	return authorID, nil
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorID := strings.TrimSpace(r.Header.Get("X-Author-ID"))
		if authorID != "" {
			r = r.WithContext(WithAuthorID(r.Context(), authorID))
		}
		next.ServeHTTP(w, r)
	})
}
