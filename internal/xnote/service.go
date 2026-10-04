package xnote

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func Service(s *Store, action string) error {
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	label := "ai.xnote.sync"
	target := fmt.Sprintf("gui/%d", os.Getuid())
	plist := filepath.Join(home, "Library/LaunchAgents", label+".plist")
	switch action {
	case "install":
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		bin := filepath.Join(home, ".local/bin/xnote")
		if e = os.MkdirAll(filepath.Dir(bin), 0700); e != nil {
			return e
		}
		if exe != bin {
			src, e := os.Open(exe)
			if e != nil {
				return e
			}
			defer src.Close()
			dst, e := os.CreateTemp(filepath.Dir(bin), ".xnote-*")
			if e != nil {
				return e
			}
			defer os.Remove(dst.Name())
			_, e = io.Copy(dst, src)
			if e == nil {
				e = dst.Chmod(0755)
			}
			dst.Close()
			if e != nil {
				return e
			}
			if e = os.Rename(dst.Name(), bin); e != nil {
				return e
			}
		}
		log := s.path(".work/service.log")
		data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>--data</string><string>%s</string><string>watch</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>15</integer><key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string></dict></plist>`, label, xmlText(bin), xmlText(s.Root), xmlText(home+"/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"), xmlText(log), xmlText(log))
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
		return fmt.Errorf("service action must be install, start, stop, status (user %s)", strconv.Itoa(os.Getuid()))
	}
	return nil
}
