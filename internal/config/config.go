package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	StorageMemory   = "memory"
	StoragePostgres = "postgres"
)

type Config struct {
	HTTPAddr      string
	StorageDriver string
	DatabaseURL   string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:      strings.TrimSpace(os.Getenv("HTTP_ADDR")),
		StorageDriver: strings.TrimSpace(os.Getenv("STORAGE_DRIVER")),
		DatabaseURL:   strings.TrimSpace(os.Getenv("DATABASE_URL")),
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}
	if cfg.StorageDriver == "" {
		cfg.StorageDriver = StorageMemory
	}

	switch cfg.StorageDriver {
	case StorageMemory:
	case StoragePostgres:
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required when STORAGE_DRIVER is postgres")
		}
	default:
		return Config{}, fmt.Errorf("unsupported STORAGE_DRIVER %q", cfg.StorageDriver)
	}

	return cfg, nil
}
