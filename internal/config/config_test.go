package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("STORAGE_DRIVER", "")
	t.Setenv("DATABASE_URL", "")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want :8080", got.HTTPAddr)
	}
	if got.StorageDriver != "memory" {
		t.Fatalf("StorageDriver = %q, want memory", got.StorageDriver)
	}
	if got.DatabaseURL != "" {
		t.Fatalf("DatabaseURL = %q, want empty", got.DatabaseURL)
	}
}

func TestLoadNormalizesWhitespace(t *testing.T) {
	t.Setenv("HTTP_ADDR", " 127.0.0.1:9000 ")
	t.Setenv("STORAGE_DRIVER", " postgres ")
	t.Setenv("DATABASE_URL", " postgres://example/test ")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.HTTPAddr != "127.0.0.1:9000" || got.StorageDriver != "postgres" || got.DatabaseURL != "postgres://example/test" {
		t.Fatalf("Load() = %#v, want trimmed values", got)
	}
}

func TestLoadRejectsUnknownStorage(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", " unknown ")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "STORAGE_DRIVER") {
		t.Fatalf("Load() error = %v, want STORAGE_DRIVER validation error", err)
	}
}

func TestLoadRequiresDatabaseURLForPostgres(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "postgres")
	t.Setenv("DATABASE_URL", "   ")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want DATABASE_URL validation error", err)
	}
}
