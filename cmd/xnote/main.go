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
	"strconv"
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
xnote [--data FOLDER] summarize ID    Queue AI title and summary (watch/TUI must run)
xnote [--data FOLDER] download ID     Queue recording download
xnote [--data FOLDER] cloud list|show UID|import LOCAL_ID CLOUD_UID
xnote [--data FOLDER] doctor

Linux (BlueZ + mpv) and macOS supported; local offline models are optional.
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
	if e = xnote.LoadEnvironment(s.Root); e != nil {
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
		output(map[string]any{"library": s.Root, "sync": s.Status(), "transcription": s.TranscriptionStatus(), "summary": s.SummaryStatus(), "automatic_summary": s.Config().AutoSummary, "summary_concurrency": xnote.EffectiveSummaryConcurrency(s.Config()), "summary_language": s.Config().SummaryLanguage, "summary_thinking": s.Config().SummaryThinking, "automatic": s.Config().Auto, "automatic_transcription": s.Config().AutoTranscribe, "transcription_concurrency": xnote.EffectiveTranscriptionConcurrency(s.Config()), "transcription_paused": s.Config().TranscriptionPaused, "recordings": len(hits), "overview": overview})
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
		case "automatic_transcription":
			if args[1] != "true" && args[1] != "false" {
				return errors.New("automatic_transcription must be true or false")
			}
			c.AutoTranscribe = args[1] == "true"
		case "automatic_summary":
			if args[1] != "true" && args[1] != "false" {
				return errors.New("automatic_summary must be true or false")
			}
			c.AutoSummary = args[1] == "true"
		case "summary_thinking":
			if args[1] != "true" && args[1] != "false" {
				return errors.New("summary_thinking must be true or false")
			}
			c.SummaryThinking = args[1] == "true"
		case "summary_concurrency":
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 || n > 8 {
				return errors.New("summary_concurrency must be an integer between 1 and 8")
			}
			c.SummaryConcurrency = n
		case "summary_language":
			c.SummaryLanguage = args[1]
		case "transcription_concurrency":
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 || n > 16 {
				return errors.New("transcription_concurrency must be an integer between 1 and 16")
			}
			c.TranscriptionConcurrency = n
		case "transcription_paused":
			if args[1] != "true" && args[1] != "false" {
				return errors.New("transcription_paused must be true or false")
			}
			c.TranscriptionPaused = args[1] == "true"
		case "doway_public_upload":
			if args[1] != "true" && args[1] != "false" {
				return errors.New("doway_public_upload must be true or false")
			}
			c.DOWAYPublicUpload = args[1] == "true"
		case "provider":
			if c.Provider != args[1] && args[1] == "elevenlabs" {
				c.APIURL, c.APIKeyEnv, c.Model = "", "", ""
			}
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
	case "download":
		if len(args) != 1 {
			return errors.New("download requires ID")
		}
		return s.Queue("download", args[0])
	case "transcribe":
		if len(args) != 1 {
			return errors.New("transcribe requires ID")
		}
		r, e := s.Get(args[0])
		if e != nil {
			return e
		}
		if r.Audio == "" || r.Trashed {
			return errors.New("transcription requires a downloaded, non-trashed recording")
		}
		return s.Update(args[0], func(r *xnote.Record) {
			if r.State != "transcribing" {
				r.State, r.Error = "queued", ""
			}
		})
	case "summarize":
		if len(args) != 1 {
			return errors.New("summarize requires ID")
		}
		return s.QueueSummary(args[0])
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
