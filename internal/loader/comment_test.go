package loader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
)

type countingBatchSource struct {
	calls atomic.Int32
	mu    sync.Mutex
	keys  [][]repository.CommentPageKey
}

type batchSourceFunc func(context.Context, []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error)

func (f batchSourceFunc) ListCommentPages(ctx context.Context, keys []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
	return f(ctx, keys)
}

func (s *countingBatchSource) ListCommentPages(ctx context.Context, keys []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.calls.Add(1)
	s.mu.Lock()
	s.keys = append(s.keys, append([]repository.CommentPageKey(nil), keys...))
	s.mu.Unlock()
	pages := make(map[repository.CommentPageKey]pagination.Page[domain.Comment], len(keys))
	for _, key := range keys {
		pages[key] = pagination.Page[domain.Comment]{}
	}
	return pages, nil
}

func TestCommentLoaderBatchesRootAndReplyPages(t *testing.T) {
	source := &countingBatchSource{}
	commentLoader := NewCommentLoader(source)
	postID := uuid.New()
	keys := []repository.CommentPageKey{
		{PostID: postID, Root: true, First: 10},
		{PostID: postID, ParentID: uuid.New(), First: 10},
		{PostID: postID, ParentID: uuid.New(), First: 10},
	}

	errs := make(chan error, len(keys))
	for _, key := range keys {
		key := key
		go func() {
			_, err := commentLoader.Load(t.Context(), key)
			errs <- err
		}()
	}
	for range keys {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if got := source.calls.Load(); got != 1 {
		t.Fatalf("batch calls = %d, want 1", got)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.keys) != 1 || len(source.keys[0]) != len(keys) {
		t.Fatalf("batches = %#v, want one batch containing %d keys", source.keys, len(keys))
	}
}

func TestCommentLoaderPropagatesRequestCancellation(t *testing.T) {
	called := make(chan struct{}, 1)
	commentLoader := NewCommentLoader(batchSourceFunc(func(ctx context.Context, _ []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
		called <- struct{}{}
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := commentLoader.Load(ctx, repository.CommentPageKey{PostID: uuid.New(), Root: true, First: 10})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
	select {
	case <-called:
	default:
		t.Fatal("batch source was not called")
	}
}

func TestMiddlewareCreatesRequestScopedLoader(t *testing.T) {
	source := &countingBatchSource{}
	handler := Middleware(source, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		commentLoader, err := FromContext(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := commentLoader.Load(r.Context(), repository.CommentPageKey{PostID: uuid.New(), Root: true, First: 10}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))

	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/query", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	if got := source.calls.Load(); got != 2 {
		t.Fatalf("batch calls = %d, want 2 request-local loads", got)
	}
}
