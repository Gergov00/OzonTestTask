package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"testozon/internal/domain"
	"testozon/internal/pagination"
	"testozon/internal/repository"
)

type Repository struct {
	pool *pgxpool.Pool
}

var _ repository.Repository = (*Repository)(nil)

func New(ctx context.Context, dsn string) (*Repository, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		configureTypeMap(conn.TypeMap())
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if _, err := pool.Exec(ctx, initialMigration); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply postgres migration: %w", err)
	}

	return &Repository{pool: pool}, nil
}

func configureTypeMap(typeMap *pgtype.Map) {
	typeMap.RegisterType(&pgtype.Type{
		Name:  "timestamptz",
		OID:   pgtype.TimestamptzOID,
		Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
	})
}

func (r *Repository) Close() {
	r.pool.Close()
}

func (r *Repository) CreatePost(ctx context.Context, post domain.Post) (domain.Post, error) {
	const query = `
		INSERT INTO posts (id, author_id, title, content, comments_enabled, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := r.pool.Exec(ctx, query, post.ID, post.AuthorID, post.Title, post.Content, post.CommentsEnabled, post.CreatedAt); err != nil {
		return domain.Post{}, fmt.Errorf("create post %s: %w", post.ID, err)
	}
	return post, nil
}

func (r *Repository) GetPost(ctx context.Context, id uuid.UUID) (domain.Post, error) {
	const query = `
		SELECT id, author_id, title, content, comments_enabled, created_at
		FROM posts
		WHERE id = $1`

	post, err := scanPost(r.pool.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Post{}, fmt.Errorf("get post %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Post{}, fmt.Errorf("get post %s: %w", id, err)
	}
	return post, nil
}

func (r *Repository) ListPosts(ctx context.Context, request pagination.PageRequest) (pagination.Page[domain.Post], error) {
	afterTime, afterID, err := pageCursor(request)
	if err != nil {
		return pagination.Page[domain.Post]{}, err
	}

	const query = `
		SELECT id, author_id, title, content, comments_enabled, created_at
		FROM posts
		WHERE ($1::timestamptz IS NULL OR (created_at, id) < ($1, $2))
		ORDER BY created_at DESC, id DESC
		LIMIT $3`

	rows, err := r.pool.Query(ctx, query, afterTime, afterID, request.First+1)
	if err != nil {
		return pagination.Page[domain.Post]{}, fmt.Errorf("list posts: %w", err)
	}
	defer rows.Close()

	posts := make([]domain.Post, 0, request.First+1)
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return pagination.Page[domain.Post]{}, fmt.Errorf("scan post page: %w", err)
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return pagination.Page[domain.Post]{}, fmt.Errorf("read post page: %w", err)
	}

	return makePostPage(posts, request.First), nil
}

func (r *Repository) SetCommentsEnabled(ctx context.Context, id uuid.UUID, enabled bool) (domain.Post, error) {
	const query = `
		UPDATE posts
		SET comments_enabled = $2
		WHERE id = $1
		RETURNING id, author_id, title, content, comments_enabled, created_at`

	post, err := scanPost(r.pool.QueryRow(ctx, query, id, enabled))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Post{}, fmt.Errorf("set comments enabled for post %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Post{}, fmt.Errorf("set comments enabled for post %s: %w", id, err)
	}
	return post, nil
}

func (r *Repository) CreateComment(ctx context.Context, comment domain.Comment) (domain.Comment, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Comment{}, fmt.Errorf("begin create comment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var commentsEnabled bool
	err = tx.QueryRow(ctx, `SELECT comments_enabled FROM posts WHERE id = $1 FOR SHARE`, comment.PostID).Scan(&commentsEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Comment{}, fmt.Errorf("create comment for post %s: %w", comment.PostID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Comment{}, fmt.Errorf("lock post %s for comment: %w", comment.PostID, err)
	}
	if !commentsEnabled {
		return domain.Comment{}, fmt.Errorf("create comment for post %s: %w", comment.PostID, domain.ErrCommentsDisabled)
	}

	if comment.ParentID != nil {
		var parentPostID uuid.UUID
		err = tx.QueryRow(ctx, `SELECT post_id FROM comments WHERE id = $1`, *comment.ParentID).Scan(&parentPostID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Comment{}, fmt.Errorf("create reply to comment %s: %w", *comment.ParentID, domain.ErrNotFound)
		}
		if err != nil {
			return domain.Comment{}, fmt.Errorf("get parent comment %s: %w", *comment.ParentID, err)
		}
		if parentPostID != comment.PostID {
			return domain.Comment{}, fmt.Errorf("%w: parent comment belongs to another post", domain.ErrValidation)
		}
	}

	const insert = `
		INSERT INTO comments (id, post_id, parent_id, author_id, text, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := tx.Exec(ctx, insert, comment.ID, comment.PostID, comment.ParentID, comment.AuthorID, comment.Text, comment.CreatedAt); err != nil {
		return domain.Comment{}, fmt.Errorf("insert comment %s: %w", comment.ID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Comment{}, fmt.Errorf("commit comment %s: %w", comment.ID, err)
	}
	return comment, nil
}

func (r *Repository) GetComment(ctx context.Context, id uuid.UUID) (domain.Comment, error) {
	const query = `
		SELECT id, post_id, parent_id, author_id, text, created_at
		FROM comments
		WHERE id = $1`

	comment, err := scanComment(r.pool.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Comment{}, fmt.Errorf("get comment %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Comment{}, fmt.Errorf("get comment %s: %w", id, err)
	}
	return comment, nil
}

func (r *Repository) ListComments(ctx context.Context, postID uuid.UUID, parentID *uuid.UUID, request pagination.PageRequest) (pagination.Page[domain.Comment], error) {
	afterTime, afterID, err := pageCursor(request)
	if err != nil {
		return pagination.Page[domain.Comment]{}, err
	}
	if err := r.ensurePostExists(ctx, postID); err != nil {
		return pagination.Page[domain.Comment]{}, err
	}

	const rootQuery = `
		SELECT id, post_id, parent_id, author_id, text, created_at
		FROM comments
		WHERE post_id = $1 AND parent_id IS NULL
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC
		LIMIT $4`
	const repliesQuery = `
		SELECT id, post_id, parent_id, author_id, text, created_at
		FROM comments
		WHERE post_id = $1 AND parent_id = $2
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC
		LIMIT $5`

	var rows pgx.Rows
	if parentID == nil {
		rows, err = r.pool.Query(ctx, rootQuery, postID, afterTime, afterID, request.First+1)
	} else {
		rows, err = r.pool.Query(ctx, repliesQuery, postID, *parentID, afterTime, afterID, request.First+1)
	}
	if err != nil {
		return pagination.Page[domain.Comment]{}, fmt.Errorf("list comments for post %s: %w", postID, err)
	}
	defer rows.Close()

	comments := make([]domain.Comment, 0, request.First+1)
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return pagination.Page[domain.Comment]{}, fmt.Errorf("scan comment page: %w", err)
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return pagination.Page[domain.Comment]{}, fmt.Errorf("read comment page: %w", err)
	}

	return makeCommentPage(comments, request.First), nil
}

type commentPageGroup struct {
	Root  bool
	First int
	After string
}

type commentParent struct {
	PostID   uuid.UUID
	ParentID uuid.UUID
}

func (r *Repository) ListCommentPages(ctx context.Context, keys []repository.CommentPageKey) (map[repository.CommentPageKey]pagination.Page[domain.Comment], error) {
	pages := make(map[repository.CommentPageKey]pagination.Page[domain.Comment], len(keys))
	if len(keys) == 0 {
		return pages, nil
	}

	postIDs := make([]uuid.UUID, 0, len(keys))
	seenPosts := make(map[uuid.UUID]struct{}, len(keys))
	seenKeys := make(map[repository.CommentPageKey]struct{}, len(keys))
	groups := make(map[commentPageGroup][]repository.CommentPageKey)
	for _, key := range keys {
		if err := key.Validate(); err != nil {
			return nil, err
		}
		pages[key] = pagination.Page[domain.Comment]{Items: []domain.Comment{}}
		if _, seen := seenKeys[key]; seen {
			continue
		}
		seenKeys[key] = struct{}{}
		group := commentPageGroup{Root: key.Root, First: key.First, After: key.After}
		groups[group] = append(groups[group], key)
		if _, ok := seenPosts[key.PostID]; !ok {
			seenPosts[key.PostID] = struct{}{}
			postIDs = append(postIDs, key.PostID)
		}
	}

	existing, err := r.existingPostIDs(ctx, postIDs)
	if err != nil {
		return nil, err
	}
	for _, postID := range postIDs {
		if _, ok := existing[postID]; !ok {
			return nil, fmt.Errorf("list comments for post %s: %w", postID, domain.ErrNotFound)
		}
	}

	for group, groupKeys := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := r.loadCommentPageGroup(ctx, group, groupKeys, pages); err != nil {
			return nil, err
		}
	}
	return pages, nil
}

func (r *Repository) existingPostIDs(ctx context.Context, postIDs []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM posts WHERE id = ANY($1::uuid[])`, postIDs)
	if err != nil {
		return nil, fmt.Errorf("check posts for comment pages: %w", err)
	}
	defer rows.Close()

	existing := make(map[uuid.UUID]struct{}, len(postIDs))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan post for comment pages: %w", err)
		}
		existing[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read posts for comment pages: %w", err)
	}
	return existing, nil
}

func (r *Repository) loadCommentPageGroup(ctx context.Context, group commentPageGroup, keys []repository.CommentPageKey, pages map[repository.CommentPageKey]pagination.Page[domain.Comment]) error {
	afterTime, afterID, err := pageCursor(pagination.PageRequest{First: group.First, After: group.After})
	if err != nil {
		return err
	}

	const rootQuery = `
		WITH requested(post_id) AS (
			SELECT unnest($1::uuid[])
		), ranked AS (
			SELECT c.id, c.post_id, c.parent_id, c.author_id, c.text, c.created_at,
				ROW_NUMBER() OVER (PARTITION BY c.post_id ORDER BY c.created_at DESC, c.id DESC) AS row_num
			FROM comments c
			JOIN requested r ON r.post_id = c.post_id
			WHERE c.parent_id IS NULL
			  AND ($2::timestamptz IS NULL OR (c.created_at, c.id) < ($2, $3))
		)
		SELECT id, post_id, parent_id, author_id, text, created_at
		FROM ranked
		WHERE row_num <= $4
		ORDER BY post_id, created_at DESC, id DESC`
	const repliesQuery = `
		WITH requested(post_id, parent_id) AS (
			SELECT * FROM unnest($1::uuid[], $2::uuid[])
		), ranked AS (
			SELECT c.id, c.post_id, c.parent_id, c.author_id, c.text, c.created_at,
				ROW_NUMBER() OVER (PARTITION BY c.post_id, c.parent_id ORDER BY c.created_at DESC, c.id DESC) AS row_num
			FROM comments c
			JOIN requested r ON r.post_id = c.post_id AND r.parent_id = c.parent_id
			WHERE ($3::timestamptz IS NULL OR (c.created_at, c.id) < ($3, $4))
		)
		SELECT id, post_id, parent_id, author_id, text, created_at
		FROM ranked
		WHERE row_num <= $5
		ORDER BY post_id, parent_id, created_at DESC, id DESC`

	var rows pgx.Rows
	if group.Root {
		postIDs := make([]uuid.UUID, 0, len(keys))
		for _, key := range keys {
			postIDs = append(postIDs, key.PostID)
		}
		rows, err = r.pool.Query(ctx, rootQuery, postIDs, afterTime, afterID, group.First+1)
	} else {
		postIDs := make([]uuid.UUID, 0, len(keys))
		parentIDs := make([]uuid.UUID, 0, len(keys))
		for _, key := range keys {
			postIDs = append(postIDs, key.PostID)
			parentIDs = append(parentIDs, key.ParentID)
		}
		rows, err = r.pool.Query(ctx, repliesQuery, postIDs, parentIDs, afterTime, afterID, group.First+1)
	}
	if err != nil {
		return fmt.Errorf("list batched comment pages: %w", err)
	}
	defer rows.Close()

	byParent := make(map[commentParent]repository.CommentPageKey, len(keys))
	for _, key := range keys {
		byParent[commentParent{PostID: key.PostID, ParentID: key.ParentID}] = key
	}
	items := make(map[repository.CommentPageKey][]domain.Comment, len(keys))
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return fmt.Errorf("scan batched comment page: %w", err)
		}
		parentID := uuid.Nil
		if comment.ParentID != nil {
			parentID = *comment.ParentID
		}
		key, ok := byParent[commentParent{PostID: comment.PostID, ParentID: parentID}]
		if !ok {
			return fmt.Errorf("batched comment page returned an unexpected parent")
		}
		items[key] = append(items[key], comment)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read batched comment pages: %w", err)
	}
	for _, key := range keys {
		pages[key] = makeCommentPage(items[key], group.First)
	}
	return nil
}

func (r *Repository) ensurePostExists(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM posts WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check post %s: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("list comments for post %s: %w", id, domain.ErrNotFound)
	}
	return nil
}

func pageCursor(request pagination.PageRequest) (*time.Time, *uuid.UUID, error) {
	if err := pagination.ValidateFirst(request.First); err != nil {
		return nil, nil, err
	}
	if request.After == "" {
		return nil, nil, nil
	}

	at, id, err := pagination.Decode(request.After)
	if err != nil {
		return nil, nil, err
	}
	return &at, &id, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanPost(row rowScanner) (domain.Post, error) {
	var post domain.Post
	err := row.Scan(&post.ID, &post.AuthorID, &post.Title, &post.Content, &post.CommentsEnabled, &post.CreatedAt)
	return post, err
}

func scanComment(row rowScanner) (domain.Comment, error) {
	var comment domain.Comment
	err := row.Scan(&comment.ID, &comment.PostID, &comment.ParentID, &comment.AuthorID, &comment.Text, &comment.CreatedAt)
	return comment, err
}

func makePostPage(posts []domain.Post, first int) pagination.Page[domain.Post] {
	hasNextPage := len(posts) > first
	if hasNextPage {
		posts = posts[:first]
	}
	page := pagination.Page[domain.Post]{Items: posts, HasNextPage: hasNextPage}
	if len(posts) > 0 {
		last := posts[len(posts)-1]
		page.EndCursor = pagination.Encode(last.CreatedAt, last.ID)
	}
	return page
}

func makeCommentPage(comments []domain.Comment, first int) pagination.Page[domain.Comment] {
	hasNextPage := len(comments) > first
	if hasNextPage {
		comments = comments[:first]
	}
	page := pagination.Page[domain.Comment]{Items: comments, HasNextPage: hasNextPage}
	if len(comments) > 0 {
		last := comments[len(comments)-1]
		page.EndCursor = pagination.Encode(last.CreatedAt, last.ID)
	}
	return page
}
