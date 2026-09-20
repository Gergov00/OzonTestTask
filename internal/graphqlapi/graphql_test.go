package graphqlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"testozon/graph"
	"testozon/internal/auth"
	"testozon/internal/domain"
	"testozon/internal/graphqlapi"
	"testozon/internal/loader"
	"testozon/internal/pagination"
	"testozon/internal/repository"
	"testozon/internal/repository/memory"
	"testozon/internal/service"
	"testozon/internal/subscription"
)

type graphQLError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions"`
}

func TestCreatePostUsesHeaderAuthor(t *testing.T) {
	c := newTestClient(t)
	var result struct {
		CreatePost struct {
			AuthorID string
		}
	}

	c.MustPost(`mutation { createPost(input: {title: "title", content: "body"}) { authorID } }`, &result,
		client.AddHeader("X-Author-ID", "author-1"))

	if result.CreatePost.AuthorID != "author-1" {
		t.Fatalf("authorID = %q, want author-1", result.CreatePost.AuthorID)
	}
}

func TestCreatePostInputRejectsAuthorID(t *testing.T) {
	_, err := newTestClient(t).RawPost(`
		mutation {
			createPost(input: {title: "title", content: "body", authorID: "forged"}) { id }
		}`,
		client.AddHeader("X-Author-ID", "author-1"))
	if err == nil {
		t.Fatal("schema accepted authorID in CreatePostInput")
	}
	message := err.Error()
	if !strings.Contains(message, "authorID") || !strings.Contains(message, "GRAPHQL_VALIDATION_FAILED") {
		t.Fatalf("error = %q, want GraphQL validation error for authorID", message)
	}
	if strings.Contains(message, `"code":"INTERNAL"`) {
		t.Fatalf("schema validation error was relabeled INTERNAL: %s", message)
	}
}

func TestGraphQLErrorCodes(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		assertErrorCode(t, newTestClient(t),
			`mutation { createPost(input: {title: "title", content: "body"}) { id } }`,
			"UNAUTHENTICATED")
	})

	t.Run("forbidden", func(t *testing.T) {
		c := newTestClient(t)
		postID := createPost(t, c, "owner", "post")
		assertErrorCode(t, c,
			`mutation($id: ID!) { setPostCommentsEnabled(postID: $id, enabled: false) { id } }`,
			"FORBIDDEN", client.Var("id", postID), client.AddHeader("X-Author-ID", "other"))
	})

	t.Run("not found", func(t *testing.T) {
		assertErrorCode(t, newTestClient(t),
			`mutation($id: ID!) { createComment(input: {postID: $id, text: "comment"}) { id } }`,
			"NOT_FOUND", client.Var("id", uuid.NewString()), client.AddHeader("X-Author-ID", "author-1"))
	})

	t.Run("comments disabled", func(t *testing.T) {
		c := newTestClient(t)
		postID := createPost(t, c, "owner", "post")
		var disabled struct{ SetPostCommentsEnabled struct{ ID string } }
		c.MustPost(`mutation($id: ID!) { setPostCommentsEnabled(postID: $id, enabled: false) { id } }`, &disabled,
			client.Var("id", postID), client.AddHeader("X-Author-ID", "owner"))
		assertErrorCode(t, c,
			`mutation($id: ID!) { createComment(input: {postID: $id, text: "comment"}) { id } }`,
			"COMMENTS_DISABLED", client.Var("id", postID), client.AddHeader("X-Author-ID", "reader"))
	})

	t.Run("validation failed", func(t *testing.T) {
		assertErrorCode(t, newTestClient(t),
			`query { post(id: "not-a-uuid") { id } }`, "VALIDATION_FAILED")
	})
}

func TestErrorPresenterSanitizesUnknownErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("database password leaked"),
		&gqlerror.Error{Message: "database password leaked"},
	} {
		presented := graphqlapi.ErrorPresenter(t.Context(), err)
		if presented.Message != "internal error" {
			t.Fatalf("message = %q, want internal error", presented.Message)
		}
		if presented.Extensions["code"] != "INTERNAL" {
			t.Fatalf("code = %v, want INTERNAL", presented.Extensions["code"])
		}
	}
}

func TestErrorPresenterLogsOriginalError(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	graphqlapi.ErrorPresenter(t.Context(), errors.New("repository connection failed"))

	got := logs.String()
	if !strings.Contains(got, "graphql_error") || !strings.Contains(got, "repository connection failed") {
		t.Fatalf("log = %q, want original GraphQL error", got)
	}
}

func TestErrorPresenterPreservesSchemaValidationErrors(t *testing.T) {
	want := &gqlerror.Error{
		Message:    "field is not defined",
		Extensions: map[string]any{"code": "GRAPHQL_VALIDATION_FAILED"},
	}
	got := graphqlapi.ErrorPresenter(t.Context(), want)
	if got.Message != want.Message || got.Extensions["code"] != "GRAPHQL_VALIDATION_FAILED" {
		t.Fatalf("presented = %#v, want schema validation error unchanged", got)
	}
}

func TestPostsPaginationReturnsTwoDisjointPages(t *testing.T) {
	c := newTestClient(t)
	created := map[string]bool{}
	for _, title := range []string{"first", "second", "third"} {
		created[createPost(t, c, "author", title)] = true
	}

	type pageResult struct {
		Posts struct {
			Nodes    []struct{ ID string }
			PageInfo struct {
				HasNextPage bool
				EndCursor   *string
			}
		}
	}
	var first pageResult
	c.MustPost(`query { posts(first: 2) { nodes { id } pageInfo { hasNextPage endCursor } } }`, &first)
	if len(first.Posts.Nodes) != 2 || !first.Posts.PageInfo.HasNextPage || first.Posts.PageInfo.EndCursor == nil {
		t.Fatalf("first page = %#v", first.Posts)
	}

	var second pageResult
	c.MustPost(`query($after: String) { posts(first: 2, after: $after) { nodes { id } pageInfo { hasNextPage endCursor } } }`, &second,
		client.Var("after", *first.Posts.PageInfo.EndCursor))
	if len(second.Posts.Nodes) != 1 || second.Posts.PageInfo.HasNextPage {
		t.Fatalf("second page = %#v", second.Posts)
	}

	seen := map[string]bool{}
	for _, page := range [][]struct{ ID string }{first.Posts.Nodes, second.Posts.Nodes} {
		for _, post := range page {
			if seen[post.ID] {
				t.Fatalf("post %s occurs on both pages", post.ID)
			}
			seen[post.ID] = true
		}
	}
	if len(seen) != len(created) {
		t.Fatalf("returned IDs = %v, created IDs = %v", seen, created)
	}
}

func TestPostCommentTree(t *testing.T) {
	c := newTestClient(t)
	postID := createPost(t, c, "post-author", "post")
	rootID := createComment(t, c, "comment-author", postID, nil, "root")
	createComment(t, c, "reply-author", postID, &rootID, "reply")

	var result struct {
		Post *struct {
			Comments struct {
				Nodes []struct {
					ID      string
					Replies struct {
						Nodes []struct {
							ParentID *string
							Text     string
						}
					}
				}
			}
		}
	}
	c.MustPost(`query($id: ID!) {
		post(id: $id) {
			comments(first: 10) {
				nodes { id replies(first: 10) { nodes { parentID text } } }
			}
		}
	}`, &result, client.Var("id", postID))

	if result.Post == nil || len(result.Post.Comments.Nodes) != 1 {
		t.Fatalf("post comments = %#v", result.Post)
	}
	replies := result.Post.Comments.Nodes[0].Replies.Nodes
	if len(replies) != 1 || replies[0].ParentID == nil || *replies[0].ParentID != rootID || replies[0].Text != "reply" {
		t.Fatalf("replies = %#v", replies)
	}
}

func TestPostCommentsBatchAcrossPostsAndIsolateRequests(t *testing.T) {
	store := &countingRepository{Repository: memory.New()}
	c := newTestClientWithRepository(t, store, store)
	firstPostID := createPost(t, c, "author", "first")
	secondPostID := createPost(t, c, "author", "second")
	createComment(t, c, "commenter", firstPostID, nil, "first root")
	createComment(t, c, "commenter", secondPostID, nil, "second root")

	query := `query { posts(first: 10) { nodes { comments(first: 10) { nodes { text } } } } }`
	for requestNumber := int32(1); requestNumber <= 2; requestNumber++ {
		var result struct {
			Posts struct {
				Nodes []struct {
					Comments struct{ Nodes []struct{ Text string } }
				}
			}
		}
		c.MustPost(query, &result)
		if len(result.Posts.Nodes) != 2 {
			t.Fatalf("request %d returned %d posts, want 2", requestNumber, len(result.Posts.Nodes))
		}
		if got := store.batchCalls.Load(); got != requestNumber {
			t.Fatalf("after request %d batch calls = %d, want %d", requestNumber, got, requestNumber)
		}
	}
}

func TestCommentRepliesBatchAcrossRootComments(t *testing.T) {
	store := &countingRepository{Repository: memory.New()}
	c := newTestClientWithRepository(t, store, store)
	postID := createPost(t, c, "author", "post")
	firstRootID := createComment(t, c, "commenter", postID, nil, "first root")
	secondRootID := createComment(t, c, "commenter", postID, nil, "second root")
	createComment(t, c, "commenter", postID, &firstRootID, "first reply")
	createComment(t, c, "commenter", postID, &secondRootID, "second reply")

	var result struct {
		Post struct {
			Comments struct {
				Nodes []struct {
					ID      string
					Replies struct{ Nodes []struct{ Text string } }
				}
			}
		}
	}
	c.MustPost(`query($postID: ID!) {
		post(id: $postID) {
			comments(first: 10) {
				nodes { id replies(first: 10) { nodes { text } } }
			}
		}
	}`, &result, client.Var("postID", postID))

	if got := store.batchCalls.Load(); got != 2 {
		t.Fatalf("batch calls = %d, want one root batch and one combined reply batch", got)
	}
	wantReplies := map[string]string{firstRootID: "first reply", secondRootID: "second reply"}
	if len(result.Post.Comments.Nodes) != len(wantReplies) {
		t.Fatalf("root comments = %#v", result.Post.Comments.Nodes)
	}
	for _, root := range result.Post.Comments.Nodes {
		if len(root.Replies.Nodes) != 1 || root.Replies.Nodes[0].Text != wantReplies[root.ID] {
			t.Fatalf("replies for root %s = %#v, want %q", root.ID, root.Replies.Nodes, wantReplies[root.ID])
		}
	}
}

func TestMissingPostIsNullWithoutGraphQLError(t *testing.T) {
	c := newTestClient(t)
	var result struct {
		Post *struct{ ID string }
	}
	if err := c.Post(`query($id: ID!) { post(id: $id) { id } }`, &result, client.Var("id", uuid.NewString())); err != nil {
		t.Fatalf("query missing post: %v", err)
	}
	if result.Post != nil {
		t.Fatalf("post = %#v, want nil", result.Post)
	}
}

func newTestClient(t *testing.T) *client.Client {
	t.Helper()
	store := memory.New()
	return newTestClientWithRepository(t, store, store)
}

func newTestClientWithRepository(t *testing.T, store repository.Repository, batchSource loader.BatchSource) *client.Client {
	t.Helper()
	broker := subscription.New(4)
	svc := service.New(store, broker, func() time.Time {
		return time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	})
	executable := graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{Service: svc, Broker: broker}})
	server := handler.NewDefaultServer(executable)
	server.SetErrorPresenter(graphqlapi.ErrorPresenter)
	return client.New(auth.Middleware(loader.Middleware(batchSource, server)))
}

type countingRepository struct {
	repository.Repository
	batchCalls atomic.Int32
}

func (r *countingRepository) ListCommentPages(ctx context.Context, keys []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
	r.batchCalls.Add(1)
	return r.Repository.ListCommentPages(ctx, keys)
}

func createPost(t *testing.T, c *client.Client, authorID, title string) string {
	t.Helper()
	var result struct{ CreatePost struct{ ID string } }
	c.MustPost(`mutation($title: String!) { createPost(input: {title: $title, content: "body"}) { id } }`, &result,
		client.Var("title", title), client.AddHeader("X-Author-ID", authorID))
	return result.CreatePost.ID
}

func createComment(t *testing.T, c *client.Client, authorID, postID string, parentID *string, text string) string {
	t.Helper()
	var result struct{ CreateComment struct{ ID string } }
	c.MustPost(`mutation($postID: ID!, $parentID: ID, $text: String!) {
		createComment(input: {postID: $postID, parentID: $parentID, text: $text}) { id }
	}`, &result, client.Var("postID", postID), client.Var("parentID", parentID), client.Var("text", text),
		client.AddHeader("X-Author-ID", authorID))
	return result.CreateComment.ID
}

func assertErrorCode(t *testing.T, c *client.Client, query, want string, options ...client.Option) {
	t.Helper()
	response, err := c.RawPost(query, options...)
	if err != nil {
		t.Fatal(err)
	}
	errs := decodeErrors(t, response.Errors)
	if len(errs) != 1 {
		t.Fatalf("errors = %#v, want one error with code %s", errs, want)
	}
	if got := errs[0].Extensions["code"]; got != want {
		t.Fatalf("code = %v, want %s (error: %s)", got, want, errs[0].Message)
	}
}

func decodeErrors(t *testing.T, raw json.RawMessage) []graphQLError {
	t.Helper()
	var errs []graphQLError
	if err := json.Unmarshal(raw, &errs); err != nil {
		t.Fatalf("decode errors: %v (raw: %s)", err, raw)
	}
	return errs
}
