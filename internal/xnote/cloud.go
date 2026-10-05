package xnote

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The example keeps clean checkouts buildable. A local app_profile.json, when
// present at build time, supplies the optional DOWAY application signing key.
//
//go:embed app_profile*.json
var appProfiles embed.FS

// Reuse the transport so consecutive cloud requests share TLS connections.
// Credentials are only sent to the configured origin, never across redirects.
var cloudHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy:               nil,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type cloudSession struct {
	PlayerID json.Number `json:"playerId"`
	Token    string      `json:"token"`
}

func decodeCloudSession(data []byte) (cloudSession, error) {
	var session cloudSession
	if err := json.Unmarshal(data, &session); err != nil {
		return session, errors.New("invalid DOWAY session")
	}
	id, err := session.PlayerID.Int64()
	if err != nil || id <= 0 || strings.TrimSpace(session.Token) == "" {
		return session, errors.New("DOWAY response missing session")
	}
	return session, nil
}

func (s *Store) cloudSession() (cloudSession, error) {
	data, err := os.ReadFile(s.path(".work/doway-session.json"))
	if err != nil {
		return cloudSession{}, errors.New("DOWAY login required")
	}
	return decodeCloudSession(data)
}

func cloudSigningKey(profiles fs.FS) (string, error) {
	b, err := fs.ReadFile(profiles, "app_profile.json")
	if errors.Is(err, fs.ErrNotExist) {
		b, err = fs.ReadFile(profiles, "app_profile.example.json")
	}
	if err != nil {
		return "", fmt.Errorf("DOWAY app profile: %w", err)
	}
	var profile struct {
		Key string `json:"signing_key"`
	}
	if err := json.Unmarshal(b, &profile); err != nil {
		return "", errors.New("invalid DOWAY app profile")
	}
	if strings.TrimSpace(profile.Key) == "" {
		return "", errors.New("DOWAY signing key not configured; supply internal/xnote/app_profile.json and rebuild (see README)")
	}
	return profile.Key, nil
}

func signBody(body map[string]any, key string, timestamp int64) map[string]any {
	result := map[string]any{}
	for k, v := range body {
		result[k] = v
	}
	result["timestamp"] = timestamp
	keys := []string{}
	for k := range result {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := []string{}
	for _, k := range keys {
		switch v := result[k].(type) {
		case string:
			pairs = append(pairs, k+"="+v)
		case int, int64, float64:
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
		case json.Number:
			// Dart decodes playerId as an integer. Preserve its decimal text;
			// converting through float64 can round it or use exponent notation.
			pairs = append(pairs, k+"="+v.String())
		}
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(strings.Join(pairs, "&")))
	result["signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return result
}
func cloudPost(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
	key, e := cloudSigningKey(appProfiles)
	if e != nil {
		return nil, e
	}
	return cloudPostRequest(ctx, cloudHTTPClient, "https://www.dowayai.com:8443"+path, body, key)
}

func cloudPostRequest(ctx context.Context, client *http.Client, endpoint string, body map[string]any, key string) (json.RawMessage, error) {
	b, e := json.Marshal(signBody(body, key, time.Now().UnixMilli()))
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Dart/3.10 (dart:io)")
	resp, e := client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("DOWAY connection failed: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DOWAY %s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	var result struct {
		Code *int            `json:"code"`
		Ret  *int            `json:"ret"`
		Data json.RawMessage `json:"data"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result); e != nil {
		return nil, fmt.Errorf("DOWAY %s: invalid response: %w", req.URL.Path, e)
	}
	if result.Code == nil {
		return nil, fmt.Errorf("DOWAY %s: response missing status code", req.URL.Path)
	}
	if *result.Code != 200 {
		var sub json.Number
		_ = json.Unmarshal(result.Data, &sub)
		code, _ := strconv.Atoi(sub.String())
		reason := "request rejected"
		if req.URL.Path == "/api/device/verify_device" {
			if result.Ret != nil && *result.Ret == 1 {
				reason = "connect the recording device in the DOWAY app"
			} else if *result.Code == 500 {
				reason = "insufficient transcription allowance"
			}
			return nil, &dowayRejectedError{Stage: "verification", Reason: reason}
		}
		if req.URL.Path == "/api/player/login" {
			if known := map[int]string{-100: "email account not found; check account / region", -200: "password rejected", -300: "account cancelled", -301: "account cancelled"}[code]; known != "" {
				reason = known
			}
		}
		return nil, fmt.Errorf("DOWAY %s: %d / %d: %s", req.URL.Path, *result.Code, code, reason)
	}
	return result.Data, nil
}
func (s *Store) Login(ctx context.Context, email, password string) error {
	return s.login(ctx, email, password, cloudPost)
}

func (s *Store) login(ctx context.Context, email, password string, post func(context.Context, string, map[string]any) (json.RawMessage, error)) error {
	if strings.TrimSpace(email) == "" || password == "" {
		return errors.New("DOWAY email and password required")
	}
	identityPath := s.path(".work/client-id")
	identity, e := os.ReadFile(identityPath)
	if os.IsNotExist(e) {
		identity = []byte(fmt.Sprintf("xnote-%d", time.Now().UnixNano()))
		if e = atomicWrite(identityPath, identity); e != nil {
			return e
		}
	} else if e != nil {
		return fmt.Errorf("DOWAY client identity: %w", e)
	}
	if len(bytes.TrimSpace(identity)) == 0 {
		return errors.New("DOWAY client identity is empty")
	}
	mobile := map[string]any{"platform": "macos", "deviceBrand": "Apple", "systemModel": runtime.GOARCH, "systemLanguage": "en", "systemVersion": runtime.GOOS, "appVersion": "3.7.9", "uuid": string(identity)}
	// The original app trims the account field, but sends the password as typed.
	data, e := post(ctx, "/api/player/login", map[string]any{"account": strings.TrimSpace(email), "passWd": password, "mobile": mobile})
	if e != nil {
		return e
	}
	session, e := decodeCloudSession(data)
	if e != nil {
		return e
	}
	if e = atomicJSON(s.path(".work/doway-session.json"), session); e != nil {
		return e
	}
	return atomicJSON(s.path(".work/cloud.json"), []any{})
}
func (s *Store) Cloud(ctx context.Context) ([]map[string]any, error) {
	session, e := s.cloudSession()
	if e != nil {
		return nil, e
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	for page := 1; page <= 1000; page++ {
		data, e := cloudPost(ctx, "/api/cloud/get_files", map[string]any{"pageNum": page, "pageSize": 100, "playerId": session.PlayerID, "token": session.Token})
		if e != nil {
			return nil, e
		}
		var batch []map[string]any
		if e = json.Unmarshal(data, &batch); e != nil {
			return nil, e
		}
		for _, r := range batch {
			id, ok := r["audioFileUID"]
			if !ok {
				return nil, errors.New("unexpected cloud recording identity")
			}
			key := fmt.Sprint(id)
			if seen[key] {
				return nil, errors.New("cloud pagination repeated")
			}
			seen[key] = true
			rows = append(rows, r)
		}
		if len(batch) < 100 {
			if e = atomicJSON(s.path(".work/cloud.json"), rows); e != nil {
				return nil, e
			}
			return rows, nil
		}
	}
	return nil, errors.New("cloud pagination exceeded limit")
}
