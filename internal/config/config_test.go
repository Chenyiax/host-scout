package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPrecedenceAndValidation(t *testing.T) {
	t.Setenv("HOST_SCOUT_BASE_URL", "https://env.example/v1")
	t.Setenv("HOST_SCOUT_MODEL", "env-model")
	t.Setenv("HOST_SCOUT_API_KEY", "secret")
	cfg, err := Parse([]string{"--model", "flag-model", "--base-url", "https://flag.example/v1/", "--max-steps", "3"}, io.Discard)
	if err != nil || cfg.Model != "flag-model" || cfg.BaseURL != "https://flag.example/v1" || cfg.APIKey != "secret" || cfg.MaxSteps != 3 {
		t.Fatalf("%+v %v", cfg, err)
	}
	cfg, err = Parse(nil, io.Discard)
	if err != nil || cfg.Model != "env-model" || cfg.MaxSteps != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
	for _, args := range [][]string{
		{"--model", ""}, {"--max-steps", "-1"},
		{"--base-url", "ftp://example"}, {"--base-url", "no-url"}, {"--base-url", "https://user:secret@example"},
		{"--base-url", "https://example?secret=x"}, {"--base-url", "https://example#fragment"},
		{"unexpected"}, {"--api-key", "secret"},
	} {
		if _, err := Parse(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	t.Setenv("HOST_SCOUT_MODEL", "")
	t.Setenv("HOST_SCOUT_BASE_URL", "")
	cfg, err = Parse([]string{"--model", "test"}, io.Discard)
	if err != nil || cfg.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestExplicitTOMLAndPrecedence(t *testing.T) {
	t.Setenv("HOST_SCOUT_MODEL", "")
	t.Setenv("HOST_SCOUT_BASE_URL", "")
	t.Setenv("HOST_SCOUT_API_KEY", "key-from-env")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = 'file-model'\nbase_url = 'https://file.example/v1'\nmax_steps = 12\nallow_insecure_http = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]string{"--config", path}, io.Discard)
	if err != nil || cfg.Model != "file-model" || cfg.BaseURL != "https://file.example/v1" || cfg.MaxSteps != 12 || !cfg.AllowInsecureHTTP || cfg.APIKey != "key-from-env" {
		t.Fatalf("file: %+v %v", cfg, err)
	}
	t.Setenv("HOST_SCOUT_MODEL", "env-model")
	t.Setenv("HOST_SCOUT_BASE_URL", "https://env.example/v1")
	cfg, err = Parse([]string{"--config", path, "--max-steps", "0", "--allow-insecure-http=false"}, io.Discard)
	if err != nil || cfg.Model != "env-model" || cfg.BaseURL != "https://env.example/v1" || cfg.MaxSteps != 0 || cfg.AllowInsecureHTTP {
		t.Fatalf("env and explicit zero/false: %+v %v", cfg, err)
	}
	cfg, err = Parse([]string{"--config", path, "--model", "flag-model", "--base-url", "https://flag.example/v1", "--max-steps", "150"}, io.Discard)
	if err != nil || cfg.Model != "flag-model" || cfg.BaseURL != "https://flag.example/v1" || cfg.MaxSteps != 150 {
		t.Fatalf("flags or 150: %+v %v", cfg, err)
	}
	if err := os.WriteFile(path, []byte("model = 'file-model'\nmax_steps = 150\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Parse([]string{"--config", path}, io.Discard)
	if err != nil || cfg.MaxSteps != 150 {
		t.Fatalf("file 150: %+v %v", cfg, err)
	}
}

func TestInvalidTOMLDoesNotRevealValues(t *testing.T) {
	t.Setenv("HOST_SCOUT_MODEL", "fallback-model")
	t.Setenv("HOST_SCOUT_BASE_URL", "")
	root := t.TempDir()
	for _, tc := range []struct{ name, body string }{
		{"syntax", "model = 'secret-value"},
		{"unknown", "api_key = 'secret-value'"},
		{"wrong type", "max_steps = 'secret-value'"},
		{"negative", "max_steps = -1"},
		{"overflow", "max_steps = 9223372036854775808"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name+".toml")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Parse([]string{"--config", path}, io.Discard)
			if err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("accepted or revealed bad config: %v", err)
			}
		})
	}
	if _, err := Parse([]string{"--config", filepath.Join(root, "missing.toml")}, io.Discard); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := Parse([]string{"--model", "fallback-model", "--max-steps", "9223372036854775808"}, io.Discard); err == nil {
		t.Fatal("flag overflow accepted")
	}
}

func TestRemoteHTTPRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("HOST_SCOUT_BASE_URL", "")
	t.Setenv("HOST_SCOUT_MODEL", "")
	_, err := Parse([]string{"--model", "test", "--base-url", "http://model.internal/v1"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--allow-insecure-http") {
		t.Fatalf("remote HTTP was not rejected: %v", err)
	}
	if _, err := Parse([]string{"--model", "test", "--base-url", "http://model.internal/v1", "--allow-insecure-http"}, io.Discard); err != nil {
		t.Fatalf("explicitly opted-in endpoint was rejected: %v", err)
	}
	if _, err := Parse([]string{"--model", "test", "--base-url", "http://localhost:8000/v1"}, io.Discard); err != nil {
		t.Fatalf("loopback endpoint was rejected: %v", err)
	}
}
