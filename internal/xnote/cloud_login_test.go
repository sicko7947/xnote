package xnote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCloudLoginPreservesPasswordAndIntegerSession(t *testing.T) {
	for _, tc := range []struct{ id, signature string }{
		{"1234567", "vYr9CMH9wBuDe4jcjEEEywNNggqrDpVw8ph2rcaYDuc="},
		{"9007199254740993", "dp/xSairGMkO0OEW2XQGAJugrI4LEwGoH8Hp9CcqVts="},
	} {
		t.Run(tc.id, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			post := func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
				if path != "/api/player/login" || body["account"] != "user@example.com" || body["passWd"] != " pass & word " {
					t.Fatal("login modified the password or used the wrong account/endpoint")
				}
				return json.RawMessage(`{"playerId":` + tc.id + `,"token":"test-token","unused":"profile"}`), nil
			}
			if err := s.login(context.Background(), " user@example.com ", " pass & word ", post); err != nil {
				t.Fatal(err)
			}
			session, err := s.cloudSession()
			if err != nil {
				t.Fatal(err)
			}
			if session.PlayerID.String() != tc.id {
				t.Fatal("session ID lost precision")
			}
			signed := signBody(map[string]any{"pageNum": 1, "pageSize": 100, "playerId": session.PlayerID, "token": session.Token}, "test-key", 123)
			if signed["signature"] != tc.signature {
				t.Fatal("session ID was not signed as an exact decimal integer")
			}
			data, err := os.ReadFile(s.path(".work/doway-session.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"playerId": `+tc.id) || strings.Contains(string(data), "unused") || strings.Contains(string(data), "pass & word") {
				t.Fatal("stored session contains unexpected fields or an inexact ID")
			}
		})
	}
}

func TestCloudLoginDoesNotSendWithoutReadableIdentity(t *testing.T) {
	for _, invalid := range []string{"directory", "empty"} {
		t.Run(invalid, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if invalid == "directory" {
				err = os.Mkdir(s.path(".work/client-id"), 0700)
			} else {
				err = os.WriteFile(s.path(".work/client-id"), nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			post := func(context.Context, string, map[string]any) (json.RawMessage, error) {
				t.Fatal("sent credentials despite invalid local identity")
				return nil, nil
			}
			if err := s.login(context.Background(), "user@example.com", "password", post); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestCloudSessionRejectsInvalidSuccessResponse(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, `{"playerId":1,"token":""}`, `{"playerId":0,"token":"test-token"}`,
		`{"playerId":1.5,"token":"test-token"}`, `{"playerId":1,"token":123}`,
	} {
		if _, err := decodeCloudSession([]byte(data)); err == nil {
			t.Fatal("accepted invalid session")
		}
	}
}

func TestCloudPostRequestUsesAppEnvelopeAndSeparatesCloudErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect cloud method or content type")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body["timestamp"]) == 0 || len(body["signature"]) == 0 || len(body["playerId"]) == 0 {
			t.Error("missing signed cloud fields")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":500,"data":-200}`)
	}))
	defer server.Close()
	_, err := cloudPostRequest(context.Background(), server.Client(), server.URL+"/api/cloud/get_files", map[string]any{"playerId": json.Number("1234567")}, "test-key")
	if err == nil || !strings.Contains(err.Error(), "/api/cloud/get_files") || strings.Contains(err.Error(), "password rejected") {
		t.Fatal("cloud errors must identify the failed endpoint instead of claiming login failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cloudPostRequest(ctx, server.Client(), server.URL, map[string]any{}, "test-key")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cloud request discarded cancellation cause")
	}
}

func TestCloudVerificationDistinguishesExplicitDenialFromUnknownOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		rejected bool
		success  bool
	}{
		{"success null data", 200, `{"code":200,"data":null}`, false, true},
		{"success false data", 200, `{"code":200,"data":false}`, false, true},
		{"quota denied", 200, `{"code":500}`, true, false},
		{"device denied", 200, `{"code":403,"ret":1}`, true, false},
		{"http failure", 500, `{"code":500}`, false, false},
		{"missing code", 200, `{}`, false, false},
		{"malformed code", 200, `{"code":"500"}`, false, false},
		{"malformed body", 200, `{`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := cloudPostRequest(context.Background(), server.Client(), server.URL+"/api/device/verify_device", map[string]any{}, "test-key")
			var denied *dowayRejectedError
			if errors.As(err, &denied) != tc.rejected || (err == nil) != tc.success {
				t.Fatalf("wrong denial classification: %v", err)
			}
		})
	}
}
