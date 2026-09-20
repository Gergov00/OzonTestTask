package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/coder/websocket"
	"github.com/google/uuid"

	"testozon/internal/config"
	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
	"testozon/internal/repository/memory"
	"testozon/internal/service"
	"testozon/internal/subscription"
)

func TestNewHandlerHealth(t *testing.T) {
	handler := newHandler(newTestDependencies())
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if strings.TrimSpace(response.Body.String()) != "ok" {
		t.Fatalf("body = %q, want ok", response.Body.String())
	}
}

func TestNewHandlerGraphQLQuery(t *testing.T) {
	handler := newHandler(newTestDependencies())
	body := []byte(`{"query":"{ posts(first: 1) { nodes { id } } }"}`)
	request := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var result struct {
		Data struct {
			Posts struct {
				Nodes []struct{ ID string }
			}
		}
		Errors []json.RawMessage
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Errors) != 0 || len(result.Data.Posts.Nodes) != 0 {
		t.Fatalf("response = %s, want empty posts without errors", response.Body.String())
	}
}

func TestNewHandlerHTTPProvidesOperationLoader(t *testing.T) {
	store := memory.New()
	postID := uuid.New()
	_, err := store.CreatePost(t.Context(), domain.Post{
		ID:              postID,
		AuthorID:        "owner",
		Title:           "title",
		Content:         "body",
		CommentsEnabled: true,
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	_, err = store.CreateComment(t.Context(), domain.Comment{
		ID:        uuid.New(),
		PostID:    postID,
		AuthorID:  "author",
		Text:      "comment",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	broker := subscription.New(4)
	handler := newHandler(Dependencies{
		Repository: store,
		Service:    service.New(store, broker, time.Now),
		Broker:     broker,
	})
	c := client.New(handler)
	c.SetCustomTarget("/query")
	var result struct {
		Post struct {
			Comments struct{ Nodes []struct{ Text string } }
		}
	}

	err = c.Post(fmt.Sprintf(`query { post(id: %q) { comments(first: 10) { nodes { text } } } }`, postID), &result)
	if err != nil {
		t.Fatalf("query comments: %v", err)
	}
	comments := result.Post.Comments.Nodes
	if len(comments) != 1 || comments[0].Text != "comment" {
		t.Fatalf("comments = %#v, want one comment", comments)
	}
}

func TestNewHandlerBatchesCommentsWithinHTTPOperation(t *testing.T) {
	base := memory.New()
	for _, title := range []string{"first", "second"} {
		postID := uuid.New()
		_, err := base.CreatePost(t.Context(), domain.Post{
			ID:              postID,
			AuthorID:        "owner",
			Title:           title,
			Content:         "body",
			CommentsEnabled: true,
			CreatedAt:       time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("create post: %v", err)
		}
		_, err = base.CreateComment(t.Context(), domain.Comment{
			ID:        uuid.New(),
			PostID:    postID,
			AuthorID:  "author",
			Text:      title,
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("create comment: %v", err)
		}
	}
	store := &countingRepository{Repository: base}
	broker := subscription.New(4)
	handler := newHandler(Dependencies{
		Repository: store,
		Service:    service.New(store, broker, time.Now),
		Broker:     broker,
	})
	c := client.New(handler)
	c.SetCustomTarget("/query")
	var result struct {
		Posts struct {
			Nodes []struct {
				Comments struct{ Nodes []struct{ Text string } }
			}
		}
	}

	err := c.Post(`query { posts(first: 2) { nodes { comments(first: 2) { nodes { text } } } } }`, &result)
	if err != nil {
		t.Fatalf("query comments: %v", err)
	}
	if len(result.Posts.Nodes) != 2 {
		t.Fatalf("posts = %#v, want two posts", result.Posts.Nodes)
	}
	if calls := store.commentPageCalls.Load(); calls != 1 {
		t.Fatalf("ListCommentPages calls = %d, want one batch", calls)
	}
}

func TestNewHandlerUsesHeaderAuthor(t *testing.T) {
	handler := newHandler(newTestDependencies())
	c := client.New(handler)
	c.SetCustomTarget("/query")
	var result struct {
		CreatePost struct{ AuthorID string }
	}

	err := c.Post(`mutation { createPost(input: {title: "title", content: "body"}) { authorID } }`, &result,
		client.AddHeader("X-Author-ID", " author-http "))
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	if result.CreatePost.AuthorID != "author-http" {
		t.Fatalf("authorID = %q, want author-http", result.CreatePost.AuthorID)
	}
}

func TestNewHandlerWebsocketUsesHeaderAuthor(t *testing.T) {
	handler := newHandler(newTestDependencies())
	c := client.New(handler)
	c.SetCustomTarget("/query")
	var result struct {
		CreatePost struct{ AuthorID string }
	}

	err := c.WebsocketOnce(
		`mutation { createPost(input: {title: "title", content: "body"}) { authorID } }`,
		&result,
		client.AddHeader("X-Author-ID", "author-websocket"),
	)
	if err != nil {
		t.Fatalf("create post over websocket: %v", err)
	}
	if result.CreatePost.AuthorID != "author-websocket" {
		t.Fatalf("authorID = %q, want author-websocket", result.CreatePost.AuthorID)
	}
}

func TestNewHandlerIsolatesLoadersPerWebsocketOperation(t *testing.T) {
	store := memory.New()
	postID := uuid.New()
	_, err := store.CreatePost(t.Context(), domain.Post{
		ID:              postID,
		AuthorID:        "owner",
		Title:           "title",
		Content:         "body",
		CommentsEnabled: true,
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	broker := subscription.New(4)
	handler := newHandler(Dependencies{
		Repository: store,
		Service:    service.New(store, broker, time.Now),
		Broker:     broker,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dialCtx, cancelDial := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/query", &websocket.DialOptions{
		HTTPHeader:   http.Header{"X-Author-ID": []string{"author"}},
		Subprotocols: []string{"graphql-transport-ws"},
	})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()
	writeWebsocketMessage(t, conn, map[string]any{"type": "connection_init"})
	readWebsocketType(t, conn, "connection_ack")

	commentsQuery := fmt.Sprintf(`query { post(id: %q) { comments(first: 10) { nodes { text } } } }`, postID)
	writeWebsocketMessage(t, conn, websocketOperation("query-before", commentsQuery))
	var before struct {
		Data struct {
			Post struct {
				Comments struct{ Nodes []struct{ Text string } }
			}
		}
	}
	readWebsocketOperation(t, conn, "query-before", &before)
	if len(before.Data.Post.Comments.Nodes) != 0 {
		t.Fatalf("comments before mutation = %#v, want empty", before.Data.Post.Comments.Nodes)
	}

	mutation := fmt.Sprintf(`mutation { createComment(input: {postID: %q, text: "new"}) { id } }`, postID)
	writeWebsocketMessage(t, conn, websocketOperation("mutation", mutation))
	var created struct {
		Data struct{ CreateComment struct{ ID string } }
	}
	readWebsocketOperation(t, conn, "mutation", &created)
	if created.Data.CreateComment.ID == "" {
		t.Fatal("mutation did not create a comment")
	}

	writeWebsocketMessage(t, conn, websocketOperation("query-after", commentsQuery))
	var after struct {
		Data struct {
			Post struct {
				Comments struct{ Nodes []struct{ Text string } }
			}
		}
	}
	readWebsocketOperation(t, conn, "query-after", &after)
	comments := after.Data.Post.Comments.Nodes
	if len(comments) != 1 || comments[0].Text != "new" {
		t.Fatalf("comments after mutation = %#v, want one new comment", comments)
	}
}

func TestNewHandlerRejectsOverlyComplexQuery(t *testing.T) {
	store := memory.New()
	broker := subscription.New(4)
	handler := newHandler(Dependencies{
		Repository: store,
		Service:    service.New(store, broker, time.Now),
		Broker:     broker,
	})
	var fields strings.Builder
	for i := range 201 {
		fmt.Fprintf(&fields, `p%d: createPost(input: {title: "title", content: "body"}) { id } `, i)
	}
	query := "mutation { " + fields.String() + " }"
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatalf("encode query: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Author-ID", "author")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if !strings.Contains(response.Body.String(), `"errors"`) {
		t.Fatalf("response = %s, want complexity error", response.Body.String())
	}
	posts, err := store.ListPosts(t.Context(), pagination.PageRequest{First: 100})
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts.Items) != 0 {
		t.Fatalf("created %d posts from rejected query, want 0", len(posts.Items))
	}
}

func TestNewHandlerScalesComplexityByPageSize(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantError bool
		wantCalls int32
	}{
		{
			name:      "small nested page",
			query:     `{ posts(first: 1) { nodes { comments(first: 1) { nodes { replies(first: 1) { nodes { id } } } } } } }`,
			wantError: false,
			wantCalls: 1,
		},
		{
			name:      "nested maximum pages",
			query:     `{ posts(first: 100) { nodes { comments(first: 100) { nodes { replies(first: 100) { nodes { id } } } } } } }`,
			wantError: true,
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &countingRepository{Repository: memory.New()}
			broker := subscription.New(4)
			handler := newHandler(Dependencies{
				Repository: store,
				Service:    service.New(store, broker, time.Now),
				Broker:     broker,
			})
			body, err := json.Marshal(map[string]string{"query": tt.query})
			if err != nil {
				t.Fatalf("encode query: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			hasError := strings.Contains(response.Body.String(), `"errors"`)
			if hasError != tt.wantError {
				t.Fatalf("response = %s, wantError = %v", response.Body.String(), tt.wantError)
			}
			if calls := store.listPostsCalls.Load(); calls != tt.wantCalls {
				t.Fatalf("ListPosts calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestPageComplexityClampsAndSaturates(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name  string
		child int
		first int
		want  int
	}{
		{name: "minimum", child: 3, first: -1, want: 4},
		{name: "maximum", child: 3, first: 101, want: 301},
		{name: "overflow", child: maxInt, first: 100, want: maxInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pageComplexity(tt.child, tt.first, nil); got != tt.want {
				t.Fatalf("pageComplexity(%d, %d) = %d, want %d", tt.child, tt.first, got, tt.want)
			}
		})
	}
}

func TestNewHandlerRejectsCrossOriginWebsocket(t *testing.T) {
	handler := newHandler(newTestDependencies())
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previousLogOutput := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	request, err := http.NewRequest(http.MethodGet, server.URL+"/query", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Origin", "https://attacker.example")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("websocket upgrade request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("cross-origin websocket upgrade was accepted")
	}
}

func TestRunStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := run(ctx, config.Config{HTTPAddr: "127.0.0.1:0", StorageDriver: config.StorageMemory})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestServeClosesActiveSubscriptionOnCancellation(t *testing.T) {
	store := memory.New()
	postID := uuid.New()
	_, err := store.CreatePost(t.Context(), domain.Post{
		ID:              postID,
		AuthorID:        "owner",
		Title:           "title",
		Content:         "body",
		CommentsEnabled: true,
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	broker := subscription.New(4)
	deps := Dependencies{
		Repository: store,
		Service:    service.New(store, broker, time.Now),
		Broker:     broker,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, deps) }()

	dialCtx, stopDial := context.WithTimeout(t.Context(), 2*time.Second)
	defer stopDial()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+listener.Addr().String()+"/query", &websocket.DialOptions{
		Subprotocols: []string{"graphql-ws"},
	})
	if err != nil {
		cancel()
		<-done
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()
	writeWebsocketMessage(t, conn, map[string]any{"type": "connection_init"})
	readWebsocketType(t, conn, "connection_ack")
	writeWebsocketMessage(t, conn, map[string]any{
		"id":   "subscription-1",
		"type": "start",
		"payload": map[string]any{
			"query": fmt.Sprintf(`subscription { commentAdded(postID: %q) { id } }`, postID),
		},
	})

	publishCtx, stopPublishing := context.WithCancel(context.Background())
	t.Cleanup(stopPublishing)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-publishCtx.Done():
				return
			case <-ticker.C:
				broker.Publish(domain.Comment{ID: uuid.New(), PostID: postID, AuthorID: "author", Text: "event", CreatedAt: time.Now().UTC()})
			}
		}
	}()
	readWebsocketType(t, conn, "data")
	stopPublishing()

	cancel()
	closeCtx, stopCloseWait := context.WithTimeout(t.Context(), 2*time.Second)
	defer stopCloseWait()
	for {
		_, _, readErr := conn.Read(closeCtx)
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, context.DeadlineExceeded) {
			t.Fatalf("websocket remained open after cancellation: %v", readErr)
		}
		break
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after cancellation")
	}
}

func newTestDependencies() Dependencies {
	store := memory.New()
	broker := subscription.New(4)
	return Dependencies{
		Repository: store,
		Service: service.New(store, broker, func() time.Time {
			return time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
		}),
		Broker: broker,
	}
}

type countingRepository struct {
	repository.Repository
	listPostsCalls   atomic.Int32
	commentPageCalls atomic.Int32
}

func (r *countingRepository) ListPosts(ctx context.Context, request pagination.PageRequest) (pagination.Page[domain.Post], error) {
	r.listPostsCalls.Add(1)
	return r.Repository.ListPosts(ctx, request)
}

func (r *countingRepository) ListCommentPages(ctx context.Context, keys []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
	r.commentPageCalls.Add(1)
	return r.Repository.ListCommentPages(ctx, keys)
}

func writeWebsocketMessage(t *testing.T, conn *websocket.Conn, message any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("encode websocket message: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write websocket message: %v", err)
	}
}

func readWebsocketType(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var message struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode websocket message: %v", err)
		}
		if message.Type == want {
			return
		}
	}
}

func websocketOperation(id, query string) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "subscribe",
		"payload": map[string]any{
			"query": query,
		},
	}
}

func readWebsocketOperation(t *testing.T, conn *websocket.Conn, id string, target any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read websocket operation %s: %v", id, err)
		}
		var message struct {
			ID      string          `json:"id"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode websocket operation %s: %v", id, err)
		}
		if message.ID != id {
			continue
		}
		if message.Type == "error" {
			t.Fatalf("websocket operation %s failed: %s", id, message.Payload)
		}
		if message.Type != "next" {
			continue
		}
		if err := json.Unmarshal(message.Payload, target); err != nil {
			t.Fatalf("decode websocket operation %s payload: %v", id, err)
		}
		return
	}
}
