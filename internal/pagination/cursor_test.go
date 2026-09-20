package pagination

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"testozon/internal/domain"
)

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	gotAt, gotID, err := Decode(Encode(at, id))
	if err != nil || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip: at=%v id=%v err=%v", gotAt, gotID, err)
	}
}

func TestValidateFirst(t *testing.T) {
	for _, first := range []int{0, 101} {
		if err := ValidateFirst(first); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("first=%d: got %v", first, err)
		}
	}
}

func TestDecodeRejectsInvalidCursors(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{
			name:   "invalid base64",
			cursor: "%",
		},
		{
			name:   "unsupported version",
			cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"createdAt":"2026-01-02T03:04:05Z","id":"11111111-1111-1111-1111-111111111111"}`)),
		},
		{
			name:   "invalid timestamp",
			cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"createdAt":"not-a-time","id":"11111111-1111-1111-1111-111111111111"}`)),
		},
		{
			name:   "invalid UUID",
			cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"createdAt":"2026-01-02T03:04:05Z","id":"not-a-uuid"}`)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Decode(tt.cursor)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("Decode(%q) error = %v, want validation error", tt.cursor, err)
			}
		})
	}
}
