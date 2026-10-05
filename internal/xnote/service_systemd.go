package xnote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const systemdUnit = "xnote.service"

func systemdUnitPath(home string) string {
	return filepath.Join(home, ".config/systemd/user", systemdUnit)
}

// systemdArgument quotes one literal argument, including specifier escapes.
// ExecStart uses the colon prefix to disable environment substitution.
func systemdArgument(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(value) + `"`
}

// env execs the absolute binary without a shell; it also permits quote characters
// in its path, which systemd rejects in the executable position itself.
func systemdServiceUnit(bin, library, environmentFile string) string {
	environment := ""
	if environmentFile != "" {
		environment = "Environment=" + systemdArgument("XNOTE_ENV_FILE="+environmentFile) + "\n"
	}

	return fmt.Sprintf(`[Unit]
Description=Xnote automatic recording sync and transcription
StartLimitIntervalSec=0

[Service]
Type=simple
%sExecStart=:/usr/bin/env %s --data %s watch
Restart=always
RestartSec=15s
TimeoutStopSec=20s
WatchdogSec=90s
NotifyAccess=main
UMask=0077
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, environment, systemdArgument(bin), systemdArgument(library))
}

func runUserSystemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return output, fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		return output, fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func systemdService(s *Store, action string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch action {
	case "install":
		environmentFile, err := environmentServicePath()
		if err != nil {
			return err
		}
		bin := filepath.Join(home, ".local/bin/xnote")
		if err = installServiceBinary(bin); err != nil {
			return err
		}
		if err = atomicWrite(systemdUnitPath(home), []byte(systemdServiceUnit(bin, s.Root, environmentFile))); err != nil {
			return err
		}
		if _, err = runUserSystemctl("daemon-reload"); err != nil {
			return err
		}
		if _, err = runUserSystemctl("enable", systemdUnit); err != nil {
			return err
		}
		_, err = runUserSystemctl("restart", systemdUnit)
		return err
	case "start", "stop":
		_, err = runUserSystemctl(action, systemdUnit)
		return err
	case "status":
		output, err := runUserSystemctl("status", "--no-pager", "--full", systemdUnit)
		fmt.Print(string(output))
		return err
	default:
		return fmt.Errorf("service action must be install, start, stop, status")
	}
}
