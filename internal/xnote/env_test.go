package xnote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvironment(t *testing.T) {
	t.Setenv("XNOTE_ENV_FILE", "")
	t.Setenv("XNOTE_TEST_EXISTING", "shell")
	const key = "XNOTE_TEST_FROM_FILE"
	os.Unsetenv(key)
	t.Cleanup(func() { os.Unsetenv(key) })
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, ".env"), []byte("# private\nXNOTE_TEST_EXISTING=file\nexport "+key+"='literal $(not-executed) $HOME'\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvironment(dir); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("XNOTE_TEST_EXISTING") != "shell" || os.Getenv(key) != "literal $(not-executed) $HOME" {
		t.Fatal("environment precedence or literal parsing failed")
	}
}

func TestInvalidEnvironmentDoesNotLeakOrPartiallyLoad(t *testing.T) {
	t.Setenv("XNOTE_ENV_FILE", "")
	const key = "XNOTE_TEST_PARTIAL_LOAD"
	os.Unsetenv(key)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=first\ninvalid secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := LoadEnvironment(dir)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unsafe/missing parse error: %v", err)
	}
	if _, exists := os.LookupEnv(key); exists {
		t.Fatal("partially loaded invalid file")
	}
}
