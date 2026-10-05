package xnote

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func Service(s *Store, action string) error {
	switch runtime.GOOS {
	case "linux":
		return systemdService(s, action)
	case "darwin":
		return launchAgentService(s, action)
	default:
		return fmt.Errorf("background service is unsupported on %s", runtime.GOOS)
	}
}

// ServiceInstalled reports whether this user's service definition exists.
func ServiceInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	var path string
	switch runtime.GOOS {
	case "linux":
		path = systemdUnitPath(home)
	case "darwin":
		path = filepath.Join(home, "Library/LaunchAgents/ai.xnote.sync.plist")
	default:
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// installServiceBinary replaces the executable atomically, including while it runs.
func installServiceBinary(bin string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe == bin {
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		return err
	}
	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.CreateTemp(filepath.Dir(bin), ".xnote-*")
	if err != nil {
		return err
	}
	defer os.Remove(dst.Name())
	_, err = io.Copy(dst, src)
	if err == nil {
		err = dst.Chmod(0755)
	}
	if err == nil {
		err = dst.Sync()
	}
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(dst.Name(), bin)
}

// environmentServicePath persists only an explicitly selected file location,
// resolved before the service loses the installer's working directory.
func environmentServicePath() (string, error) {
	path := os.Getenv("XNOTE_ENV_FILE")
	if path == "" {
		return "", nil
	}
	return filepath.Abs(path)
}

func launchAgentEnvironment(home, environmentFile string) string {
	data := "<key>PATH</key><string>" + xmlText(home+"/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin") + "</string>"
	if environmentFile != "" {
		data += "<key>XNOTE_ENV_FILE</key><string>" + xmlText(environmentFile) + "</string>"
	}
	return data
}

func launchAgentService(s *Store, action string) error {
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	label := "ai.xnote.sync"
	target := fmt.Sprintf("gui/%d", os.Getuid())
	plist := filepath.Join(home, "Library/LaunchAgents", label+".plist")
	switch action {
	case "install":
		environmentFile, err := environmentServicePath()
		if err != nil {
			return err
		}
		bin := filepath.Join(home, ".local/bin/xnote")
		if e = installServiceBinary(bin); e != nil {
			return e
		}
		log := s.path(".work/service.log")
		data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>--data</string><string>%s</string><string>watch</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>15</integer><key>EnvironmentVariables</key><dict>%s</dict><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string></dict></plist>`, label, xmlText(bin), xmlText(s.Root), launchAgentEnvironment(home, environmentFile), xmlText(log), xmlText(log))
		if e = atomicWrite(plist, []byte(data)); e != nil {
			return e
		}
		_ = exec.Command("launchctl", "bootout", target+"/"+label).Run()
		if b, e := exec.Command("launchctl", "bootstrap", target, plist).CombinedOutput(); e != nil {
			return fmt.Errorf("launchctl: %s", b)
		}
	case "stop":
		return exec.Command("launchctl", "bootout", target+"/"+label).Run()
	case "start":
		return exec.Command("launchctl", "bootstrap", target, plist).Run()
	case "status":
		cmd := exec.Command("launchctl", "print", target+"/"+label)
		b, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("background service unavailable: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "state =") || strings.HasPrefix(line, "pid =") || strings.HasPrefix(line, "last exit code =") {
				fmt.Println(line)
			}
		}
		return nil
	default:
		return fmt.Errorf("service action must be install, start, stop, status")
	}
	return nil
}
