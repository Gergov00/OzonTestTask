package domain

import (
	"time"

	"github.com/google/uuid"
)

type Post struct {
	ID              uuid.UUID
	AuthorID        string
	Title           string
	Content         string
	CommentsEnabled bool
	CreatedAt       time.Time
}

type Comment struct {
	ID        uuid.UUID
	PostID    uuid.UUID
	ParentID  *uuid.UUID
	AuthorID  string
	Text      string
	CreatedAt time.Time
}
