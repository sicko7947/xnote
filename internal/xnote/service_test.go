package xnote

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentServicePath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, value, want string }{
		{"default", "", ""},
		{"relative", "my config/../secrets.env", filepath.Join(cwd, "secrets.env")},
		{"absolute", filepath.Join(t.TempDir(), "private env"), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XNOTE_ENV_FILE", tt.value)
			want := tt.want
			if tt.name == "absolute" {
				want = tt.value
			}
			got, err := environmentServicePath()
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("path = %q; want %q", got, want)
			}
		})
	}
}

func TestLaunchAgentEnvironmentEscaping(t *testing.T) {
	home := `/home/a & <b> "quoted"`
	file := "/config/private & < > %u $HOME \\\"quoted\\\"\nfile"
	t.Setenv("XNOTE_API_KEY", "secret-must-not-be-serialized")
	for _, environmentFile := range []string{"", file} {
		generated := "<dict>" + launchAgentEnvironment(home, environmentFile) + "</dict>"
		var decoded struct {
			Keys   []string `xml:"key"`
			Values []string `xml:"string"`
		}
		if err := xml.Unmarshal([]byte(generated), &decoded); err != nil {
			t.Fatal(err)
		}
		keys := []string{"PATH"}
		values := []string{home + "/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
		if environmentFile != "" {
			keys = append(keys, "XNOTE_ENV_FILE")
			values = append(values, environmentFile)
		}
		if !reflect.DeepEqual(decoded.Keys, keys) || !reflect.DeepEqual(decoded.Values, values) {
			t.Fatalf("invalid environment round-trip: %#v", decoded)
		}
		if strings.Contains(generated, "secret-must-not-be-serialized") {
			t.Fatal("secret leaked into service definition")
		}
	}
}
