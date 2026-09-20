//go:build integration

package postgres

import (
	"os"
	"testing"

	"testozon/internal/repository"
	"testozon/internal/repository/repositorytest"
)

func TestRepositoryContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	repositorytest.RunContractTests(t, func(t *testing.T) repository.Repository {
		t.Helper()

		repo, err := New(t.Context(), dsn)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(repo.Close)

		if _, err := repo.pool.Exec(t.Context(), "TRUNCATE comments, posts"); err != nil {
			t.Fatalf("clean database: %v", err)
		}

		return repo
	})
}
