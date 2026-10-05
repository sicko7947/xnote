package xnote

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DOWAY 3.7.7's flutter_oss_aliyun Client.init fetches these short-lived
// credentials with GET, without the signed POST envelope used by other APIs.
// Keep them in memory. Callers must validate their DOWAY session first and only
// operate on the object keys derived from that account and its own recordings.
type dowayOSSCredentials struct {
	AccessKeyID     string    `json:"AccessKeyId"`
	AccessKeySecret string    `json:"AccessKeySecret"`
	SecurityToken   string    `json:"SecurityToken"`
	Expiration      time.Time `json:"Expiration"`
}

var dowayOSSHTTPClient = &http.Client{
	Transport: cloudHTTPClient.Transport,
	Timeout:   10 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func fetchDOWAYOSSCredentials(ctx context.Context, client *http.Client) (dowayOSSCredentials, error) {
	var credentials dowayOSSCredentials
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.dowayai.com:8443/api/player/getAliyunToken", nil)
	if err != nil {
		return credentials, errors.New("DOWAY storage authorization request failed")
	}
	req.Header.Set("User-Agent", "Dart/3.10 (dart:io)")
	resp, err := dowayOSSDo(ctx, client, req)
	if err != nil {
		return credentials, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return credentials, fmt.Errorf("DOWAY storage authorization: HTTP %d", resp.StatusCode)
	}
	body, err := dowayOSSReadLimit(resp.Body, 64<<10)
	if err != nil || json.Unmarshal(body, &credentials) != nil {
		return dowayOSSCredentials{}, errors.New("DOWAY storage authorization returned invalid credentials")
	}
	if err := credentials.validate(time.Now()); err != nil {
		return dowayOSSCredentials{}, err
	}
	return credentials, nil
}

func (c dowayOSSCredentials) validate(now time.Time) error {
	for _, value := range []string{c.AccessKeyID, c.AccessKeySecret, c.SecurityToken} {
		if value == "" || len(value) > 32768 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("DOWAY storage authorization returned invalid credentials")
		}
	}
	if !c.Expiration.After(now.Add(30 * time.Second)) {
		return errors.New("DOWAY storage authorization expired; retry to refresh credentials")
	}
	return nil
}

func dowayTranscriptionBucket(region string) string {
	switch region {
	case "eu-central-1":
		return "frankxnote"
	case "us-east-1":
		return "useastxnote"
	default:
		return "asiaxnote"
	}
}

func dowayAudioObjectKey(deviceSN, playerID, fileKey string) (string, error) {
	if deviceSN == "" {
		deviceSN = "00000"
	}
	if !dowayOSSIdentity(deviceSN) || !dowayOSSPlayerID(playerID) || !dowayOSSIdentity(fileKey) {
		return "", errors.New("invalid DOWAY recording identity")
	}
	return deviceSN + playerID + fileKey + ".mp3", nil
}

func dowayTranscriptObjectKey(playerID, fileKey string) (string, error) {
	if !dowayOSSPlayerID(playerID) || !dowayOSSIdentity(fileKey) {
		return "", errors.New("invalid DOWAY recording identity")
	}
	return "trans00000/" + playerID + "_" + fileKey + "_trans.json", nil
}

func dowayOSSIdentity(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func dowayOSSPlayerID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

type dowayOSSClient struct {
	HTTP        *http.Client
	Credentials dowayOSSCredentials
	Now         func() time.Time
}

func (c dowayOSSClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func dowayOSSObjectURL(bucket, key string) (*url.URL, error) {
	switch bucket {
	case "asiaxnote", "chinaxnote", "frankxnote", "useastxnote":
	default:
		return nil, errors.New("unsupported DOWAY storage bucket")
	}
	if len(key) == 0 || len(key) > 512 || strings.Contains(key, "..") {
		return nil, errors.New("invalid DOWAY storage object key")
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" {
			return nil, errors.New("invalid DOWAY storage object key")
		}
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
				return nil, errors.New("invalid DOWAY storage object key")
			}
		}
	}
	return &url.URL{Scheme: "https", Host: bucket + ".oss-accelerate.aliyuncs.com", Path: "/" + key}, nil
}

func (c dowayOSSClient) request(ctx context.Context, method, bucket, key string, body io.Reader, headers http.Header) (*http.Request, error) {
	now := c.now()
	if err := c.Credentials.validate(now); err != nil {
		return nil, err
	}
	u, err := dowayOSSObjectURL(bucket, key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, errors.New("DOWAY storage request failed")
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	req.Header.Set("Date", now.UTC().Format(http.TimeFormat))
	req.Header.Set("X-Oss-Security-Token", c.Credentials.SecurityToken)
	req.Header.Set("User-Agent", "Dart/3.10 (dart:io)")
	canonical := method + "\n" + req.Header.Get("Content-MD5") + "\n" + req.Header.Get("Content-Type") + "\n" + req.Header.Get("Date") + "\n"
	ossHeaders := make([]string, 0)
	for name, values := range req.Header {
		name = strings.ToLower(name)
		if strings.HasPrefix(name, "x-oss-") {
			ossHeaders = append(ossHeaders, name+":"+strings.TrimSpace(strings.Join(values, ","))+"\n")
		}
	}
	sort.Strings(ossHeaders)
	canonical += strings.Join(ossHeaders, "") + "/" + bucket + "/" + key
	req.Header.Set("Authorization", "OSS "+c.Credentials.AccessKeyID+":"+dowayOSSSign(c.Credentials.AccessKeySecret, canonical))
	return req, nil
}

func dowayOSSSign(secret, canonical string) string {
	// OSS signature V1 is HMAC-SHA1, matching the original app's OSS client.
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (c dowayOSSClient) UploadFile(ctx context.Context, bucket, key, filename string) error {
	return c.UploadFileWithACL(ctx, bucket, key, filename, false)
}

// UploadFileWithACL grants anonymous object reads only when the caller has
// explicit user opt-in. DOWAY's ASR fetches public-read objects; private remains
// the default for all other uploads. Callers must remove their own object when
// the transcription reaches a terminal state to limit the exposure lifetime.
func (c dowayOSSClient) UploadFileWithACL(ctx context.Context, bucket, key, filename string, allowPublic bool) error {
	f, err := os.Open(filename)
	if err != nil {
		return errors.New("cannot open recording for DOWAY upload")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return errors.New("DOWAY upload requires a non-empty recording file")
	}
	// MD5 is only OSS's transfer-integrity checksum, not a security primitive.
	hash := md5.New()
	if _, err := io.Copy(hash, f); err != nil {
		return errors.New("cannot read recording for DOWAY upload")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.New("cannot seek recording for DOWAY upload")
	}
	headers := http.Header{
		"Content-Type":           {"audio/mpeg"},
		"Content-Md5":            {base64.StdEncoding.EncodeToString(hash.Sum(nil))},
		"Cache-Control":          {"no-cache"},
		"X-Oss-Forbid-Overwrite": {"true"},
		"X-Oss-Object-Acl":       {"private"},
		"X-Oss-Storage-Class":    {"Standard"},
	}
	if allowPublic {
		headers.Set("X-Oss-Object-Acl", "public-read")
	}
	req, err := c.request(ctx, http.MethodPut, bucket, key, f, headers)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	resp, err := dowayOSSDo(ctx, c.HTTP, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return dowayOSSStatusError("upload", resp.StatusCode)
	}
	return nil
}

// Delete removes exactly one object. The workflow must bind bucket/key to its
// validated persisted job before calling; there is no bucket or batch deletion.
func (c dowayOSSClient) Delete(ctx context.Context, bucket, key string) error {
	req, err := c.request(ctx, http.MethodDelete, bucket, key, nil, nil)
	if err != nil {
		return err
	}
	resp, err := dowayOSSDo(ctx, c.HTTP, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return dowayOSSStatusError("cleanup", resp.StatusCode)
	}
	return nil
}

func (c dowayOSSClient) Exists(ctx context.Context, bucket, key string) (bool, error) {
	req, err := c.request(ctx, http.MethodHead, bucket, key, nil, nil)
	if err != nil {
		return false, err
	}
	resp, err := dowayOSSDo(ctx, c.HTTP, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, dowayOSSStatusError("check", resp.StatusCode)
	}
}

func (c dowayOSSClient) Download(ctx context.Context, bucket, key string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > 64<<20 {
		return nil, errors.New("invalid DOWAY transcript download limit")
	}
	req, err := c.request(ctx, http.MethodGet, bucket, key, nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := dowayOSSDo(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, dowayOSSStatusError("download", resp.StatusCode)
	}
	return dowayOSSReadLimit(resp.Body, maxBytes)
}

func (c dowayOSSClient) SignedURL(bucket, key string, ttl time.Duration) (string, error) {
	now := c.now()
	if err := c.Credentials.validate(now); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", errors.New("invalid DOWAY storage URL lifetime")
	}
	u, err := dowayOSSObjectURL(bucket, key)
	if err != nil {
		return "", err
	}
	expires := now.Add(ttl)
	if limit := c.Credentials.Expiration.Add(-30 * time.Second); expires.After(limit) {
		expires = limit
	}
	seconds := strconv.FormatInt(expires.Unix(), 10)
	canonical := "GET\n\n\n" + seconds + "\n/" + bucket + "/" + key + "?security-token=" + c.Credentials.SecurityToken
	u.RawQuery = url.Values{
		"OSSAccessKeyId": {c.Credentials.AccessKeyID},
		"Expires":        {seconds},
		"Signature":      {dowayOSSSign(c.Credentials.AccessKeySecret, canonical)},
		"security-token": {c.Credentials.SecurityToken},
	}.Encode()
	return u.String(), nil
}

func dowayOSSDo(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, errors.New("DOWAY storage requires HTTPS")
	}
	switch req.URL.Host {
	case "www.dowayai.com:8443", "asiaxnote.oss-accelerate.aliyuncs.com", "chinaxnote.oss-accelerate.aliyuncs.com", "frankxnote.oss-accelerate.aliyuncs.com", "useastxnote.oss-accelerate.aliyuncs.com":
	default:
		return nil, errors.New("unsupported DOWAY storage host")
	}
	if client == nil {
		client = dowayOSSHTTPClient
	}
	// Enforce the redirect boundary even for an injected client. A redirect must
	// never forward the STS token or an upload body to a different destination.
	isolated := *client
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := isolated.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// net/url.Error embeds its URL; credentials or signed URLs must not reach
		// the engine logs or TUI through wrapped transport errors.
		return nil, errors.New("DOWAY storage connection failed")
	}
	return resp, nil
}

func dowayOSSReadLimit(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, errors.New("DOWAY storage response could not be read")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("DOWAY storage response exceeds size limit")
	}
	return data, nil
}

func dowayOSSStatusError(operation string, status int) error {
	if status == http.StatusConflict {
		return errors.New("DOWAY storage object already exists; recording was not overwritten")
	}
	return fmt.Errorf("DOWAY storage %s: HTTP %d", operation, status)
}
