package xnote

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSystemdServiceUnitLiteralPaths(t *testing.T) {
	unit := systemdServiceUnit(`/home/a b%u/$HOME/"quoted"/xnote`, "/recordings/line\nbreak\\path", "")
	want := `ExecStart=:/usr/bin/env "/home/a b%%u/$HOME/\"quoted\"/xnote" --data "/recordings/line\nbreak\\path" watch`
	if !strings.Contains(unit, want+"\n") {
		t.Fatalf("literal path escaping missing: %s", unit)
	}
	for _, directive := range []string{"Restart=always", "RestartSec=15s", "TimeoutStopSec=20s", "WatchdogSec=90s", "NotifyAccess=main", "WantedBy=default.target", "StandardOutput=journal", "UMask=0077"} {
		if !strings.Contains(unit, directive+"\n") {
			t.Errorf("missing %s", directive)
		}
	}
}

func TestServiceInstalledPlatformPath(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("service platform required")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if ServiceInstalled() {
		t.Fatal("uninstalled service reported installed")
	}
	path := systemdUnitPath(home)
	if runtime.GOOS == "darwin" {
		path = filepath.Join(home, "Library/LaunchAgents/ai.xnote.sync.plist")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if !ServiceInstalled() {
		t.Fatal("installed service not detected")
	}
}

// Ask systemd's parser to verify paths that would break unquoted units. This
// reads a temporary unit; it never loads or starts a service.
func TestSystemdServiceUnitVerify(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd requires Linux")
	}
	tool, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not installed")
	}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true executable not installed")
	}
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	bin := filepath.Join(dir, `x note %u $HOME "quoted"`)
	if err := os.Symlink(trueBin, bin); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, systemdUnit)
	if err := os.WriteFile(unit, []byte(systemdServiceUnit(bin, "/library %u $HOME \"quotes\"", "/environment %u $HOME \"quotes\"\\file")), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(tool, "--user", "verify", unit).CombinedOutput(); err != nil {
		t.Fatalf("systemd rejected generated unit: %v: %s", err, output)
	}
}

func TestSystemdEnvironmentFile(t *testing.T) {
	unit := systemdServiceUnit("/bin/true", "/library", "/environment %u $HOME \"quotes\"\\file\nnext")
	want := `Environment="XNOTE_ENV_FILE=/environment %%u $HOME \"quotes\"\\file\nnext"`
	if !strings.Contains(unit, want+"\n") {
		t.Fatalf("missing escaped environment file: %s", unit)
	}
	if strings.Contains(systemdServiceUnit("/bin/true", "/library", ""), "Environment=") {
		t.Fatal("default environment path should not be persisted")
	}
}
