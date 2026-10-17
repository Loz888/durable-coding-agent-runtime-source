package testdb

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DatabaseURL uses an exported test variable first, then the module's .env.test.
// Server DATABASE_URL and .env are deliberately not used for database tests.
func DatabaseURL(t *testing.T, key string) string {
	t.Helper()
	if key != "TEST_DATABASE_URL" && key != "DAY12_TEST_DATABASE_URL" {
		t.Fatalf("unsupported test database key %q", key)
	}
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("test working directory: %v", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	file, err := os.Open(filepath.Join(dir, ".env.test"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("open .env.test: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') ||
			(value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		return value
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read .env.test: %v", err)
	}
	return ""
}
