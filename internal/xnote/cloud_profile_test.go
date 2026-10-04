package xnote

import (
	"testing"
	"testing/fstest"
)

func TestCloudProfileIsOptionalForBuildButRequiredForRequests(t *testing.T) {
	profiles := fstest.MapFS{
		"app_profile.example.json": {Data: []byte(`{"signing_key":""}`)},
	}
	if _, err := cloudSigningKey(profiles); err == nil {
		t.Fatal("clean checkout must not send requests with an empty signing key")
	}
	profiles["app_profile.json"] = &fstest.MapFile{Data: []byte(`{"signing_key":"test-app-key"}`)}
	if key, err := cloudSigningKey(profiles); err != nil || key != "test-app-key" {
		t.Fatal("local build profile was not selected")
	}
	profiles["app_profile.json"].Data = []byte(`{invalid`)
	if _, err := cloudSigningKey(profiles); err == nil {
		t.Fatal("invalid local profile silently fell back")
	}
}
