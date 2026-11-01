package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigBoundaryAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	base := map[string]any{"authority": "test", "database": "ledger.db", "credentials": []any{map[string]any{}}}
	write := func(v map[string]any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(base)
	c, err := loadConfig(path)
	if err != nil || c.Listen != "127.0.0.1:8090" || c.Database != filepath.Join(dir, "ledger.db") {
		t.Fatalf("config: %+v, %v", c, err)
	}
	base["listen"] = "0.0.0.0:8090"
	write(base)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("network plaintext accepted: %v", err)
	}
	base["tls_cert"], base["tls_key"] = "server.crt", "server.key"
	write(base)
	c, err = loadConfig(path)
	if err != nil || c.TLSCert != filepath.Join(dir, "server.crt") {
		t.Fatalf("TLS config: %+v, %v", c, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("public config accepted")
	}
}

func TestReleaseBuildIdentity(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, buildDate
	t.Cleanup(func() { version, commit, buildDate = oldVersion, oldCommit, oldDate })
	version, commit, buildDate = "v0.1.0", "0123456789abcdef", "2026-11-01T00:00:00Z"
	if got := buildID(); got != "v0.1.0 (0123456789abcdef 2026-11-01T00:00:00Z)" {
		t.Fatalf("release identity: %s", got)
	}
}
