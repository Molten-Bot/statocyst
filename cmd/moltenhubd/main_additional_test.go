package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvBoolParsesAndFallsBack(t *testing.T) {
	const key = "MOLTENHUB_TEST_ENV_BOOL"

	t.Setenv(key, "")
	if got := envBool(key, true); !got {
		t.Fatal("expected fallback=true for empty env var")
	}

	t.Setenv(key, "TRUE")
	if got := envBool(key, false); !got {
		t.Fatal("expected parsed bool true")
	}

	t.Setenv(key, "definitely-not-a-bool")
	if got := envBool(key, false); got {
		t.Fatal("expected fallback=false when env var is invalid")
	}
}

func TestLoadDotEnvParsesSupportedFormsAndPreservesExistingValues(t *testing.T) {
	fromExportKey := "MOLTENHUB_TEST_DOTENV_FROM_EXPORT"
	quotedKey := "MOLTENHUB_TEST_DOTENV_QUOTED"
	singleQuotedKey := "MOLTENHUB_TEST_DOTENV_SINGLE_QUOTED"
	existingKey := "MOLTENHUB_TEST_DOTENV_EXISTING"

	t.Setenv(existingKey, "already-set")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := fmt.Sprintf("# comment\nexport %s=exported\n%s=\"quoted value\"\n%s='single value'\n%s=from_file\nINVALID_LINE\n", fromExportKey, quotedKey, singleQuotedKey, existingKey)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp .env: %v", err)
	}

	loadDotEnv(path)

	if got := os.Getenv(fromExportKey); got != "exported" {
		t.Fatalf("expected %s=exported, got %q", fromExportKey, got)
	}
	if got := os.Getenv(quotedKey); got != "quoted value" {
		t.Fatalf("expected %s with quotes stripped, got %q", quotedKey, got)
	}
	if got := os.Getenv(singleQuotedKey); got != "single value" {
		t.Fatalf("expected %s with quotes stripped, got %q", singleQuotedKey, got)
	}
	if got := os.Getenv(existingKey); got != "already-set" {
		t.Fatalf("expected existing env var to be preserved, got %q", got)
	}
}
