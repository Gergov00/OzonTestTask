package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestConfigureTypeMapScansTimestamptzInUTC(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("test-local", 3*60*60)
	t.Cleanup(func() { time.Local = originalLocal })

	typeMap := pgtype.NewMap()
	configureTypeMap(typeMap)

	var got time.Time
	err := typeMap.Scan(
		pgtype.TimestamptzOID,
		pgtype.TextFormatCode,
		[]byte("2026-01-02 03:04:05+03"),
		&got,
	)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if got.Location() != time.UTC {
		t.Fatalf("Scan() location = %v, want UTC", got.Location())
	}
	want := time.Date(2026, 1, 2, 0, 4, 5, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Scan() = %v, want %v", got, want)
	}
}
