package xnote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type dowayOSSTestTransport func(*http.Request) (*http.Response, error)

func (f dowayOSSTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func dowayOSSTestClient(transport dowayOSSTestTransport) dowayOSSClient {
	now := time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)
	return dowayOSSClient{
		HTTP: &http.Client{Transport: transport},
		Credentials: dowayOSSCredentials{
			AccessKeyID: "test-id", AccessKeySecret: "test-secret", SecurityToken: "test-token", Expiration: now.Add(time.Hour),
		},
		Now: func() time.Time { return now },
	}
}

func dowayOSSTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestDOWAYOSSObjectIdentity(t *testing.T) {
	key, err := dowayAudioObjectKey("SN", "72", "20261005010203")
	if err != nil || key != "SN7220261005010203.mp3" {
		t.Fatalf("wrong recording key: %q, %v", key, err)
	}
	key, err = dowayAudioObjectKey("", "72", "20261005010203")
	if err != nil || key != "000007220261005010203.mp3" {
		t.Fatalf("wrong app recording key: %q, %v", key, err)
	}
	key, err = dowayTranscriptObjectKey("72", "20261005010203")
	if err != nil || key != "trans00000/72_20261005010203_trans.json" {
		t.Fatalf("wrong transcript key: %q, %v", key, err)
	}
	for _, playerID := range []string{"", "0", "-1", "01", "+2", "7/2"} {
		if _, err := dowayAudioObjectKey("SN", playerID, "file"); err == nil {
			t.Errorf("accepted invalid player identity %q", playerID)
		}
	}
	for _, key := range []string{"", "/audio.mp3", "audio.mp3?token=secret", "../audio.mp3", "a/../audio.mp3", "a//b", "audio\r\n.mp3", "https://attacker.invalid/file"} {
		if _, err := dowayOSSObjectURL("asiaxnote", key); err == nil {
			t.Errorf("accepted invalid object key %q", key)
		}
	}
	if _, err := dowayOSSObjectURL("other-bucket", "audio.mp3"); err == nil {
		t.Fatal("accepted arbitrary bucket")
	}
}

func TestDOWAYOSSUploadSignatureAndPrivacy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "recording.mp3")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := dowayOSSTestClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPut || req.URL.Scheme != "https" || req.URL.Host != "asiaxnote.oss-accelerate.aliyuncs.com" || req.URL.Path != "/SN7220261005010203.mp3" {
			t.Fatal("wrong upload destination or method")
		}
		if req.Header.Get("Authorization") != "OSS test-id:vDdS7AxS0CJYE4pSDKOSP7PBw+8=" {
			t.Fatal("OSS signature differs from the independent HMAC-SHA1 vector")
		}
		for header, want := range map[string]string{
			"Content-Type": "audio/mpeg", "Content-MD5": "XUFAKrxLKna5cZ2REBfFkg==", "X-Oss-Object-Acl": "private",
			"X-Oss-Forbid-Overwrite": "true", "Cache-Control": "no-cache", "X-Oss-Storage-Class": "Standard",
		} {
			if req.Header.Get(header) != want {
				t.Errorf("wrong %s header", header)
			}
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || string(body) != "hello" || req.ContentLength != 5 {
			t.Fatal("audio body was changed or not streamed with its actual size")
		}
		return dowayOSSTestResponse(200, ""), nil
	})
	if err := client.UploadFile(context.Background(), "asiaxnote", "SN7220261005010203.mp3", file); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("upload was not performed exactly once")
	}
}

func TestDOWAYOSSDownloadBoundAndRedirectIsolation(t *testing.T) {
	calls := 0
	client := dowayOSSTestClient(func(req *http.Request) (*http.Response, error) {
		calls++
		resp := dowayOSSTestResponse(302, "private-server-message")
		resp.Header.Set("Location", "https://attacker.invalid/?security-token=private-token")
		return resp, nil
	})
	_, err := client.Download(context.Background(), "asiaxnote", "trans00000/72_test_trans.json", 20)
	if err == nil || calls != 1 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "attacker") {
		t.Fatal("redirect leaked credentials, followed another host, or lost status")
	}
	client.HTTP.Transport = dowayOSSTestTransport(func(req *http.Request) (*http.Response, error) {
		return dowayOSSTestResponse(200, strings.Repeat("a", 21)), nil
	})
	if _, err = client.Download(context.Background(), "asiaxnote", "result.json", 20); err == nil {
		t.Fatal("accepted oversized transcript")
	}
	client.HTTP.Transport = dowayOSSTestTransport(func(req *http.Request) (*http.Response, error) {
		return dowayOSSTestResponse(200, "{\"text\":\"hello\"}"), nil
	})
	if body, err := client.Download(context.Background(), "asiaxnote", "result.json", 20); err != nil || string(body) != "{\"text\":\"hello\"}" {
		t.Fatal("valid transcript failed to download")
	}
}

func TestDOWAYOSSUploadPublicRequiresExplicitOptIn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "recording.mp3")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, allowPublic := range []bool{false, true} {
		acl, signature := "private", "vDdS7AxS0CJYE4pSDKOSP7PBw+8="
		if allowPublic {
			acl, signature = "public-read", "fZyizAETZr+NwBB+sHVt8JLMUSg="
		}
		client := dowayOSSTestClient(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("X-Oss-Object-Acl") != acl || req.Header.Get("Authorization") != "OSS test-id:"+signature {
				t.Fatal("ACL opt-in or its authenticated signature is incorrect")
			}
			return dowayOSSTestResponse(200, ""), nil
		})
		if err := client.UploadFileWithACL(context.Background(), "asiaxnote", "SN7220261005010203.mp3", file, allowPublic); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDOWAYOSSDeleteExactObjectAndRedirectIsolation(t *testing.T) {
	calls := 0
	client := dowayOSSTestClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodDelete || req.URL.Host != "chinaxnote.oss-accelerate.aliyuncs.com" || req.URL.Path != "/trans00000/72_test_trans.json" || req.URL.RawQuery != "" || req.Body != nil {
			t.Fatal("cleanup was not limited to the exact object")
		}
		if req.Header.Get("Authorization") != "OSS test-id:xRGnZyOpb3oJ/gm5rbcgIuC1fQg=" {
			t.Fatal("cleanup signature differs from the independent HMAC-SHA1 vector")
		}
		return dowayOSSTestResponse(204, ""), nil
	})
	if err := client.Delete(context.Background(), "chinaxnote", "trans00000/72_test_trans.json"); err != nil || calls != 1 {
		t.Fatal("single-object cleanup failed")
	}
	for _, key := range []string{"", "/", "../another-object", "audio.mp3?delete"} {
		if err := client.Delete(context.Background(), "chinaxnote", key); err == nil || calls != 1 {
			t.Fatal("invalid cleanup object reached the network")
		}
	}
	client.HTTP.Transport = dowayOSSTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		resp := dowayOSSTestResponse(307, "private-response")
		resp.Header.Set("Location", "https://attacker.invalid/?security-token=private-token")
		return resp, nil
	})
	if err := client.Delete(context.Background(), "chinaxnote", "trans00000/72_test_trans.json"); err == nil || calls != 2 || strings.Contains(err.Error(), "private") {
		t.Fatal("cleanup followed a redirect or exposed server response data")
	}
}

func TestDOWAYOSSFailureBoundaries(t *testing.T) {
	calls := 0
	client := dowayOSSTestClient(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport echoed private-token https://private-url.invalid")
	})
	if _, err := client.Exists(context.Background(), "asiaxnote", "audio.mp3"); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("transport error exposed request details")
	}
	client.Credentials.Expiration = client.now()
	if _, err := client.Exists(context.Background(), "asiaxnote", "audio.mp3"); err == nil || calls != 1 {
		t.Fatal("expired credentials reached the network")
	}
	for _, endpoint := range []string{"http://asiaxnote.oss-accelerate.aliyuncs.com/audio.mp3", "https://attacker.invalid/audio.mp3"} {
		req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		if _, err := dowayOSSDo(context.Background(), client.HTTP, req); err == nil || calls != 1 {
			t.Fatal("unsafe destination reached the network")
		}
	}
	client = dowayOSSTestClient(func(req *http.Request) (*http.Response, error) {
		return dowayOSSTestResponse(404, ""), nil
	})
	if exists, err := client.Exists(context.Background(), "asiaxnote", "audio.mp3"); err != nil || exists {
		t.Fatal("missing file was not distinguished from authorization failure")
	}
	client.HTTP.Transport = dowayOSSTestTransport(func(req *http.Request) (*http.Response, error) {
		return dowayOSSTestResponse(403, ""), nil
	})
	if _, err := client.Exists(context.Background(), "asiaxnote", "audio.mp3"); err == nil {
		t.Fatal("permission denied was treated as missing file")
	}
}

func TestDOWAYOSSSignedURLExpirationAndSignature(t *testing.T) {
	client := dowayOSSTestClient(nil)
	signed, err := client.SignedURL("asiaxnote", "SN7220261005010203.mp3", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil || u.Scheme != "https" || u.Query().Get("Signature") != "/bKk4viwA+058lpz0ETlCSaQcRI=" || u.Query().Get("Expires") != "1791162423" || u.Query().Get("security-token") != "test-token" {
		t.Fatal("signed URL differs from the independent HMAC-SHA1 vector")
	}
	signed, err = client.SignedURL("asiaxnote", "audio.mp3", 30*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(signed)
	if u.Query().Get("Expires") != "1791165693" {
		t.Fatal("signed URL can outlive temporary credentials")
	}
}

func TestDOWAYOSSFetchCredentialsContract(t *testing.T) {
	credentials := dowayOSSCredentials{AccessKeyID: "temporary-test-id", AccessKeySecret: "temporary-test-secret", SecurityToken: "temporary-test-token", Expiration: time.Now().Add(time.Hour)}
	body, _ := json.Marshal(credentials)
	client := &http.Client{Transport: dowayOSSTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://www.dowayai.com:8443/api/player/getAliyunToken" || req.Body != nil {
			t.Fatal("credentials request differs from original app contract")
		}
		return dowayOSSTestResponse(200, string(body)), nil
	})}
	got, err := fetchDOWAYOSSCredentials(context.Background(), client)
	if err != nil || got.AccessKeyID != credentials.AccessKeyID || !got.Expiration.Equal(credentials.Expiration) {
		t.Fatal("temporary credentials were not parsed")
	}
	client.Transport = dowayOSSTestTransport(func(req *http.Request) (*http.Response, error) {
		return dowayOSSTestResponse(200, "{\"AccessKeyId\":\"private-malformed-value\"}"), nil
	})
	if _, err := fetchDOWAYOSSCredentials(context.Background(), client); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("malformed credentials accepted or exposed")
	}
}
