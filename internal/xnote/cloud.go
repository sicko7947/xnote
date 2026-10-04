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
	"strings"
	"time"
)

// The example keeps clean checkouts buildable. A local app_profile.json, when
// present at build time, supplies the optional DOWAY application signing key.
//
//go:embed app_profile*.json
var appProfiles embed.FS

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
	b, e := json.Marshal(signBody(body, key, time.Now().UnixMilli()))
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://www.dowayai.com:8443"+path, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Dart/3.10 (dart:io)")
	client := http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return nil, errors.New("DOWAY connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DOWAY HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result); e != nil {
		return nil, e
	}
	if result.Code != 200 {
		var sub int
		_ = json.Unmarshal(result.Data, &sub)
		reason := map[int]string{-100: "email account not found; check account / region", -200: "password rejected", -300: "account cancelled", -301: "account cancelled"}[sub]
		return nil, fmt.Errorf("DOWAY %d / %d: %s", result.Code, sub, reason)
	}
	return result.Data, nil
}
func (s *Store) Login(ctx context.Context, email, password string) error {
	identityPath := s.path(".work/client-id")
	identity, e := os.ReadFile(identityPath)
	if os.IsNotExist(e) {
		identity = []byte(fmt.Sprintf("xnote-%d", time.Now().UnixNano()))
		if e = atomicWrite(identityPath, identity); e != nil {
			return e
		}
	}
	mobile := map[string]any{"platform": "macos", "deviceBrand": "Apple", "systemModel": runtime.GOARCH, "systemLanguage": "en", "systemVersion": runtime.GOOS, "appVersion": "3.7.9", "uuid": string(identity)}
	data, e := cloudPost(ctx, "/api/player/login", map[string]any{"account": strings.TrimSpace(email), "passWd": strings.TrimSpace(password), "mobile": mobile})
	if e != nil {
		return e
	}
	var result map[string]any
	if e = json.Unmarshal(data, &result); e != nil {
		return e
	}
	if result["token"] == nil || result["playerId"] == nil {
		return errors.New("DOWAY response missing session")
	}
	if e = atomicJSON(s.path(".work/doway-session.json"), map[string]any{"playerId": result["playerId"], "token": result["token"]}); e != nil {
		return e
	}
	return atomicJSON(s.path(".work/cloud.json"), []any{})
}
func (s *Store) Cloud(ctx context.Context) ([]map[string]any, error) {
	var session map[string]any
	if e := readJSON(s.path(".work/doway-session.json"), &session); e != nil {
		return nil, errors.New("DOWAY login required")
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	for page := 1; page <= 1000; page++ {
		data, e := cloudPost(ctx, "/api/cloud/get_files", map[string]any{"pageNum": page, "pageSize": 100, "playerId": session["playerId"], "token": session["token"]})
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
