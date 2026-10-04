package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sicko7947/xnote/internal/xnote"
)

func output(v any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(v)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "xnote:", err)
		os.Exit(1)
	}
}
func run() error {
	home, _ := os.UserHomeDir()
	root := os.Getenv("XNOTE_DATA_DIR")
	if root == "" {
		root = filepath.Join(home, "Documents/XNote")
	}
	flags := flag.NewFlagSet("xnote", flag.ContinueOnError)
	flags.StringVar(&root, "data", root, "recording library folder")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, `X NOTE — automatic recording library

xnote [--data FOLDER]                 Open terminal interface
xnote [--data FOLDER] watch           Automatic Bluetooth sync + transcription
xnote [--data FOLDER] search QUERY --json
xnote [--data FOLDER] list --json
xnote [--data FOLDER] show ID --json
xnote [--data FOLDER] status
xnote [--data FOLDER] config [KEY VALUE]
xnote [--data FOLDER] transcribe ID   Queue transcription (watch/TUI must run)
xnote [--data FOLDER] service install|start|stop|status
xnote [--data FOLDER] cloud list|show UID|import LOCAL_ID CLOUD_UID
xnote [--data FOLDER] doctor

No Python runtime required. macOS arm64 binary; local offline models are optional.
Search stdout is JSON with --json; errors go to stderr. Audio and Markdown are
under recordings/YYYY/MM/DEVICE-FILENAME/. Credentials are never printed.`)
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	s, e := xnote.Open(root)
	if e != nil {
		return e
	}
	args := flags.Args()
	cmd := "tui"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch cmd {
	case "tui":
		return xnote.UI(ctx, s)
	case "watch":
		return xnote.Run(ctx, s)
	case "list", "search":
		query := []string{}
		asJSON := false
		for _, a := range args {
			if a == "--json" {
				asJSON = true
			} else {
				query = append(query, a)
			}
		}
		hits, e := s.Search(strings.Join(query, " "), false)
		if e != nil {
			return e
		}
		if asJSON {
			output(hits)
		} else {
			for _, h := range hits {
				fmt.Printf("%s  %s  [%s]\n  %s\n", h.Record.ID, h.Record.Title, h.Record.State, h.Snippet)
			}
		}
	case "show":
		if len(args) == 0 {
			return errors.New("show requires ID")
		}
		r, e := s.Get(args[0])
		if e != nil {
			return e
		}
		output(r)
	case "cloud":
		if len(args) == 0 {
			return errors.New("cloud requires list, show UID, or import LOCAL_ID CLOUD_UID")
		}
		switch args[0] {
		case "sync":
			n, e := s.SyncCloud(ctx)
			if e != nil {
				return e
			}
			output(map[string]int{"updated": n})
		case "list":
			rows, e := s.Cloud(ctx)
			if e != nil {
				return e
			}
			output(rows)
		case "show":
			if len(args) != 2 {
				return errors.New("cloud show requires UID")
			}
			r, e := s.CloudInfo(ctx, args[1])
			if e != nil {
				return e
			}
			output(r)
		case "import":
			if len(args) != 3 {
				return errors.New("cloud import requires LOCAL_ID CLOUD_UID")
			}
			return s.ImportCloud(ctx, args[1], args[2])
		default:
			return errors.New("unknown cloud operation")
		}
		return nil
	case "status":
		hits, e := s.Search("", false)
		if e != nil {
			return e
		}
		overview, e := s.Overview()
		if e != nil {
			return e
		}
		output(map[string]any{"library": s.Root, "sync": s.Status(), "automatic": s.Config().Auto, "recordings": len(hits), "overview": overview})
	case "config":
		c := s.Config()
		if len(args) == 0 {
			output(c)
			return nil
		}
		if len(args) != 2 {
			return errors.New("config requires KEY VALUE")
		}
		switch args[0] {
		case "locale":
			c.Locale = args[1]
		case "automatic":
			if args[1] != "true" && args[1] != "false" {
				return errors.New("automatic must be true or false")
			}
			c.Auto = args[1] == "true"
		case "provider":
			c.Provider = args[1]
		case "device_serial":
			c.Serial = args[1]
		case "api_url":
			c.APIURL = args[1]
		case "api_key_env":
			c.APIKeyEnv = args[1]
		case "api_model":
			c.Model = args[1]
		case "offline_model":
			c.OfflineModel = args[1]
		case "transcription_language":
			c.Language = args[1]
		default:
			return errors.New("unknown setting")
		}
		return s.SaveConfig(c)
	case "transcribe":
		if len(args) != 1 {
			return errors.New("transcribe requires ID")
		}
		return s.Update(args[0], func(r *xnote.Record) {
			if r.Audio != "" && !r.Trashed {
				r.State = "queued"
				r.Error = ""
			}
		})
	case "service":
		if len(args) != 1 {
			return errors.New("service requires an action")
		}
		return xnote.Service(s, args[0])
	case "probe-wifi":
		report, e := xnote.ProbeWiFi(ctx, s)
		output(report)
		return e
	case "doctor":
		output(xnote.Doctor(s))
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
	return nil
}
