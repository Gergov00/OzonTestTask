package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"testozon/internal/domain"
)

func TestAuthorID(t *testing.T) {
	t.Run("returns author stored in context", func(t *testing.T) {
		t.Parallel()

		ctx := WithAuthorID(t.Context(), "author-1")
		got, err := AuthorID(ctx)
		if err != nil {
			t.Fatalf("AuthorID() error = %v", err)
		}
		if got != "author-1" {
			t.Fatalf("AuthorID() = %q, want %q", got, "author-1")
		}
	})

	t.Run("rejects missing author", func(t *testing.T) {
		t.Parallel()

		_, err := AuthorID(t.Context())
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("AuthorID() error = %v, want ErrUnauthenticated", err)
		}
	})
}

func TestMiddleware(t *testing.T) {
	t.Run("stores trimmed header value", func(t *testing.T) {
		t.Parallel()

		var got string
		handler := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, _ = AuthorID(r.Context())
		}))
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("X-Author-ID", "  author-1  ")

		handler.ServeHTTP(httptest.NewRecorder(), request)

		if got != "author-1" {
			t.Fatalf("author ID = %q, want %q", got, "author-1")
		}
	})

	t.Run("leaves author absent for blank header", func(t *testing.T) {
		t.Parallel()

		var gotErr error
		handler := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, gotErr = AuthorID(r.Context())
		}))
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("X-Author-ID", " \t ")

		handler.ServeHTTP(httptest.NewRecorder(), request)

		if !errors.Is(gotErr, domain.ErrUnauthenticated) {
			t.Fatalf("AuthorID() error = %v, want ErrUnauthenticated", gotErr)
		}
	})
}
