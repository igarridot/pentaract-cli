package config

import (
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	t.Setenv("PENTARACT_EMAIL", "")

	input := strings.NewReader(`
# comment
PENTARACT_BASE_URL=http://localhost:8080
export PENTARACT_EMAIL="user@example.com"
PENTARACT_PASSWORD='secret value'
PENTARACT_STORAGE=Main Storage
PENTARACT_RETRIES=5
PENTARACT_RETRY_DELAY=3s
`)

	values, err := parseDotEnv(input)
	if err != nil {
		t.Fatalf("parseDotEnv returned error: %v", err)
	}

	if got := values["PENTARACT_BASE_URL"]; got != "http://localhost:8080" {
		t.Fatalf("base url = %q, want http://localhost:8080", got)
	}
	if got := values["PENTARACT_EMAIL"]; got != "user@example.com" {
		t.Fatalf("email = %q, want user@example.com", got)
	}
	if got := values["PENTARACT_PASSWORD"]; got != "secret value" {
		t.Fatalf("password = %q, want secret value", got)
	}
	if got := values["PENTARACT_STORAGE"]; got != "Main Storage" {
		t.Fatalf("storage = %q, want Main Storage", got)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	t.Setenv("PENTARACT_EMAIL", "override@example.com")
	t.Setenv("PENTARACT_RETRIES", "4")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Email != "override@example.com" {
		t.Fatalf("email = %q, want override@example.com", cfg.Email)
	}
	if cfg.Retries != 4 {
		t.Fatalf("retries = %d, want 4", cfg.Retries)
	}
	if cfg.SourceDir != "/source" {
		t.Fatalf("source dir = %q, want /source", cfg.SourceDir)
	}
}
