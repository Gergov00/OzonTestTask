package pagination

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"testozon/internal/domain"
)

const (
	cursorVersion = 1
	maxPageSize   = 100
)

type PageRequest struct {
	First int
	After string
}

type Page[T any] struct {
	Items       []T
	HasNextPage bool
	EndCursor   string
}

type cursorPayload struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func Encode(at time.Time, id uuid.UUID) string {
	payload, err := json.Marshal(cursorPayload{
		Version:   cursorVersion,
		CreatedAt: at.Format(time.RFC3339Nano),
		ID:        id.String(),
	})
	if err != nil {
		panic(fmt.Sprintf("marshal cursor: %v", err))
	}

	return base64.RawURLEncoding.EncodeToString(payload)
}

func Decode(cursor string) (time.Time, uuid.UUID, error) {
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, validationError("decode cursor", err)
	}

	var decoded cursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return time.Time{}, uuid.Nil, validationError("unmarshal cursor", err)
	}
	if decoded.Version != cursorVersion {
		return time.Time{}, uuid.Nil, validationError("unsupported cursor version", nil)
	}

	at, err := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
	if err != nil {
		return time.Time{}, uuid.Nil, validationError("parse cursor timestamp", err)
	}

	id, err := uuid.Parse(decoded.ID)
	if err != nil {
		return time.Time{}, uuid.Nil, validationError("parse cursor UUID", err)
	}

	return at, id, nil
}

func ValidateFirst(first int) error {
	if first < 1 || first > maxPageSize {
		return validationError("first must be between 1 and 100", nil)
	}

	return nil
}

func validationError(message string, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s: %v", domain.ErrValidation, message, err)
	}

	return fmt.Errorf("%w: %s", domain.ErrValidation, message)
}
