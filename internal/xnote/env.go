package xnote

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadEnvironment reads a private library environment file, never shell code.
// Explicit process environment wins. Missing default files are optional.
func LoadEnvironment(root string) error {
	path := os.Getenv("XNOTE_ENV_FILE")
	explicit := path != ""
	if !explicit {
		path = filepath.Join(root, ".env")
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) && !explicit {
		return nil
	}
	if err != nil {
		return fmt.Errorf("environment file: %w", err)
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		key, value, ok := strings.Cut(s, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || !environmentKey(key) {
			return fmt.Errorf("invalid environment assignment at line %d", line)
		}
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return fmt.Errorf("unclosed environment quote at line %d", line)
			}
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading environment file: %w", err)
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("setting environment variable %s: %w", key, err)
			}
		}
	}
	return nil
}

func environmentKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
