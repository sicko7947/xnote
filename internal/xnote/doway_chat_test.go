package xnote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const dowayChatTestSuccess = `{"choices":[{"index":0,"message":{"role":"assistant","content":"Useful summary."},"finish_reason":"stop"}]}`

func dowayChatTestConfig(endpoint string) dowayChatConfig {
	return dowayChatConfig{Endpoint: endpoint, Model: "fixture-model", Headers: map[string]string{"api-key": "secret-fixture", "X-Provider-Version": "test-version"}}
}

func dowayChatTestMessages() []dowayChatMessage {
	return []dowayChatMessage{{Role: "system", Content: "Summarize."}, {Role: "user", Content: "Fixture transcript."}}
}

type dowayChatTestTransport struct {
	base http.RoundTripper
	t    *testing.T
}

func (t dowayChatTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.GetBody != nil || r.ContentLength <= 0 {
		t.t.Error("chat request is replayable or has no Content-Length")
	}
	return t.base.RoundTrip(r)
}

func TestDOWAYChatWireAndSharedClientReuse(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var addresses []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu.Lock()
		addresses = append(addresses, r.RemoteAddr)
		mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.URL.Query().Get("api-version") != "test" {
			t.Error("chat method or endpoint changed")
		}
		if r.Header.Get("api-key") != "secret-fixture" || r.Header.Get("X-Provider-Version") != "test-version" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Error("chat request headers changed")
		}
		var body map[string]json.RawMessage
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error("invalid chat request JSON")
		}
		var model string
		var stream bool
		var messages []dowayChatMessage
		json.Unmarshal(body["model"], &model)
		json.Unmarshal(body["stream"], &stream)
		json.Unmarshal(body["messages"], &messages)
		if len(body) != 3 || model != "fixture-model" || stream || len(messages) != 2 || messages[0].Role != "system" || messages[1].Content != "Fixture transcript." {
			t.Error("chat request contract changed")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, dowayChatTestSuccess)
	}))
	defer server.Close()
	client := server.Client()
	client.Transport = dowayChatTestTransport{base: client.Transport, t: t}
	defer client.CloseIdleConnections()
	config := dowayChatTestConfig(server.URL + "/chat/completions?api-version=test")
	for i := 0; i < 2; i++ {
		text, e := requestDOWAYChat(context.Background(), client, config, dowayChatTestMessages(), false)
		if e != nil || text != "Useful summary." {
			t.Fatalf("chat request failed: %v", e)
		}
	}
	mu.Lock()
	reused := len(addresses) == 2 && addresses[0] == addresses[1]
	mu.Unlock()
	if !reused {
		t.Error("sequential requests did not reuse their TLS connection")
	}
	const parallel = 6
	results := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			text, e := requestDOWAYChat(context.Background(), client, config, dowayChatTestMessages(), false)
			if e == nil && text != "Useful summary." {
				e = errors.New("unexpected response text")
			}
			results <- e
		}()
	}
	for i := 0; i < parallel; i++ {
		if e := <-results; e != nil {
			t.Error(e)
		}
	}
	if calls.Load() != parallel+2 {
		t.Errorf("requests were retried: %d", calls.Load())
	}
}

func TestDOWAYChatThinkingOptionWire(t *testing.T) {
	disabled, enabled := false, true
	for _, tc := range []struct {
		name   string
		option *bool
	}{
		{"unspecified", nil},
		{"disabled", &disabled},
		{"enabled", &enabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]json.RawMessage
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error("invalid chat request JSON")
				}
				value, exists := body["enable_thinking"]
				if tc.option == nil {
					if exists || len(body) != 3 {
						t.Error("unspecified thinking changed the three-field request")
					}
				} else {
					var thinking bool
					if !exists || len(body) != 4 || json.Unmarshal(value, &thinking) != nil || thinking != *tc.option || string(value) == "null" {
						t.Error("explicit thinking option was not sent as a JSON boolean")
					}
				}
				io.WriteString(w, dowayChatTestSuccess)
			}))
			defer server.Close()
			client := server.Client()
			client.Transport = dowayChatTestTransport{base: client.Transport, t: t}
			config := dowayChatTestConfig(server.URL)
			config.EnableThinking = tc.option
			text, e := requestDOWAYChat(context.Background(), client, config, dowayChatTestMessages(), false)
			if e != nil || text != "Useful summary." || calls.Load() != 1 {
				t.Errorf("thinking request result: calls=%d, error=%v", calls.Load(), e)
			}
		})
	}
}

func TestDOWAYChatJSONOutputWire(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				value, exists := body["response_format"]
				if exists != enabled {
					t.Errorf("response_format present=%v, want %v", exists, enabled)
				}
				if enabled && string(value) != `{"type":"json_object"}` {
					t.Errorf("invalid JSON output contract: %s", value)
				}
				io.WriteString(w, dowayChatTestSuccess)
			}))
			defer server.Close()
			config := dowayChatTestConfig(server.URL)
			config.JSONOutput = enabled
			if _, err := requestDOWAYChat(context.Background(), server.Client(), config, dowayChatTestMessages(), false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDOWAYChatSSEFramingAndUTF8(t *testing.T) {
	stream := "\ufeff: keepalive\n\n" +
		"event: message\ndata: {\"choices\":[\ndata: {\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好 \"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world 🌏\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("newline-%q", newline), func(t *testing.T) {
			body := strings.ReplaceAll(stream, "\n", newline)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if r.Header.Get("Accept") != "text/event-stream" {
					t.Error("missing SSE accept header")
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				for i := range len(body) {
					if _, e := io.WriteString(w, body[i:i+1]); e != nil {
						return
					}
					w.(http.Flusher).Flush() // Split Unicode code points and CRLF across reads.
				}
			}))
			defer server.Close()
			text, e := requestDOWAYChat(context.Background(), server.Client(), dowayChatTestConfig(server.URL), dowayChatTestMessages(), true)
			if e != nil || text != "你好 world 🌏" {
				t.Fatalf("SSE framing failed: %v", e)
			}
		})
	}
}

func TestDOWAYChatSSELongLineAndCompletionForms(t *testing.T) {
	for _, completion := range []string{"data: [DONE]\n\n", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"} {
		t.Run(fmt.Sprint(len(completion)), func(t *testing.T) {
			want := strings.Repeat("x", 70<<10)
			body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + want + "\"}}]}\n\n" + completion
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, body)
			}))
			defer server.Close()
			text, e := requestDOWAYChat(context.Background(), server.Client(), dowayChatTestConfig(server.URL), dowayChatTestMessages(), true)
			if e != nil || text != want {
				t.Fatalf("long SSE content or termination failed: %v", e)
			}
		})
	}
}

func TestDOWAYChatRejectsPartialAndProviderErrors(t *testing.T) {
	partial := "data: {\"choices\":[{\"delta\":{\"content\":\"secret-fixture\"}}]}\n\n"
	stop := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	for _, tc := range []struct {
		name, body string
		stream     bool
	}{
		{"nonstream length", `{"choices":[{"message":{"content":"secret-fixture"},"finish_reason":"length"}]}`, false},
		{"nonstream filter", `{"choices":[{"message":{"content":"secret-fixture"},"finish_reason":"content_filter"}]}`, false},
		{"nonstream missing finish", `{"choices":[{"message":{"content":"secret-fixture"}}]}`, false},
		{"nonstream empty", `{"choices":[{"message":{"content":" \n "},"finish_reason":"stop"}]}`, false},
		{"nonstream null", `{"choices":[{"message":{"content":null},"finish_reason":"stop"}]}`, false},
		{"nonstream provider error", `{"error":{"message":"secret-fixture"}}`, false},
		{"nonstream trailing JSON", dowayChatTestSuccess + `{"secret":"secret-fixture"}`, false},
		{"nonstream extra choice", `{"choices":[{"message":{"content":"secret-fixture"},"finish_reason":"stop"},{"index":1,"message":{"content":"second"},"finish_reason":"stop"}]}`, false},
		{"nonstream refusal", `{"choices":[{"message":{"content":"secret-fixture","refusal":"secret-fixture"},"finish_reason":"stop"}]}`, false},
		{"nonstream tool", `{"choices":[{"message":{"content":"secret-fixture","tool_calls":[{}]},"finish_reason":"stop"}]}`, false},
		{"stream missing finish", partial, true},
		{"stream unterminated event", strings.TrimSuffix(partial, "\n"), true},
		{"stream missing final separator", partial + "data: [DONE]", true},
		{"stream empty", "data: [DONE]\n\n", true},
		{"stream length", partial + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", true},
		{"stream filter", partial + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n", true},
		{"stream tool finish", partial + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", true},
		{"stream unknown finish", partial + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"unknown\"}]}\n\n", true},
		{"stream provider error", partial + "data: {\"error\":{\"message\":\"secret-fixture\"}}\n\n", true},
		{"stream error event", partial + "event: error\ndata: secret-fixture\n\n", true},
		{"stream error after stop", partial + stop + "event: error\ndata: secret-fixture\n\n", true},
		{"stream null chunk", partial + "data: null\n\ndata: [DONE]\n\n", true},
		{"stream missing choices", partial + "data: {}\n\ndata: [DONE]\n\n", true},
		{"stream null choices", partial + "data: {\"choices\":null}\n\ndata: [DONE]\n\n", true},
		{"stream text after stop", partial + stop + partial, true},
		{"stream unterminated after stop", partial + stop + "data: secret-fixture", true},
		{"stream malformed", partial + "data: {secret-fixture}\n\n", true},
		{"stream malformed UTF8", "data: {\"choices\":[{\"delta\":{\"content\":\"\xff\"}}]}\n\ndata: [DONE]\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			text, e := requestDOWAYChat(context.Background(), server.Client(), dowayChatTestConfig(server.URL+"?token=endpoint-secret"), dowayChatTestMessages(), tc.stream)
			if e == nil || text != "" {
				t.Fatal("partial or invalid response was treated as success")
			}
			if strings.Contains(e.Error(), "secret-fixture") || strings.Contains(e.Error(), "endpoint-secret") {
				t.Error("error exposed provider credentials or response text")
			}
		})
	}
}

func TestDOWAYChatCancellationClosesBlockingStream(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		text, e := requestDOWAYChat(ctx, server.Client(), dowayChatTestConfig(server.URL), dowayChatTestMessages(), true)
		if text != "" {
			e = errors.New("cancellation returned partial text")
		}
		result <- e
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never started")
	}
	cancel()
	select {
	case e := <-result:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("wrong cancellation result: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled stream did not return")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled stream left the server connection open")
	}
}

func TestDOWAYChatDoneDoesNotWaitForServerToClose(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Complete.\",\"tool_calls\":[ ]}}]}\n\ndata: [DONE]\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	text, e := requestDOWAYChat(ctx, server.Client(), dowayChatTestConfig(server.URL), dowayChatTestMessages(), true)
	if e != nil || text != "Complete." {
		t.Fatalf("DONE did not complete the request: %v", e)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("completed SSE response was not closed")
	}
}

func TestDOWAYChatRejectsTransportTruncationAfterStop(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Incomplete.\"},\"finish_reason\":\"stop\"}]}\n\n"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)+20))
		io.WriteString(w, body) // Missing bytes must fail even though stop arrived.
	}))
	defer server.Close()
	text, e := requestDOWAYChat(context.Background(), server.Client(), dowayChatTestConfig(server.URL), dowayChatTestMessages(), true)
	if e == nil || text != "" {
		t.Fatal("truncated transport returned a partial result")
	}
}

type dowayChatCancelAtEOF struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r dowayChatCancelAtEOF) Read(p []byte) (int, error) {
	n, e := r.reader.Read(p)
	if e == io.EOF {
		r.cancel()
	}
	return n, e
}

type dowayChatRoundTripFunc func(*http.Request) (*http.Response, error)

func (f dowayChatRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDOWAYChatPreservesCompletedResponseDuringCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancellation at EOF is deterministic: the complete paid response must
	// remain available to the caller for durable storage before shutdown.
	body := `{"choices":[{"message":{"content":"Complete.","tool_calls":[ ]},"finish_reason":"stop"}]}`
	client := &http.Client{Transport: dowayChatRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.Body.Close()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(dowayChatCancelAtEOF{reader: strings.NewReader(body), cancel: cancel})}, nil
	})}
	text, e := requestDOWAYChat(ctx, client, dowayChatTestConfig("https://chat.invalid"), dowayChatTestMessages(), false)
	if e != nil || text != "Complete." || ctx.Err() != context.Canceled {
		t.Fatalf("completed response was discarded: %v", e)
	}
}

func TestDOWAYChatRedirectAndRateLimitDoNotReplay(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308, 400, 401, 403, 404, 422, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Location", "/redirect?key=secret-fixture")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				io.WriteString(w, "secret-fixture")
			}))
			defer server.Close()
			client := server.Client()
			var callerRedirects atomic.Int32
			client.CheckRedirect = func(*http.Request, []*http.Request) error { callerRedirects.Add(1); return nil }
			text, e := requestDOWAYChat(context.Background(), client, dowayChatTestConfig(server.URL+"?token=endpoint-secret"), dowayChatTestMessages(), false)
			if e == nil || text != "" || calls.Load() != 1 || callerRedirects.Load() != 0 {
				t.Error("HTTP failure was retried, redirected, or returned text")
			}
			var httpError *dowayChatHTTPError
			if !errors.As(e, &httpError) || httpError.StatusCode != status {
				t.Error("HTTP failure did not retain its typed status code")
			}
			if e != nil && (strings.Contains(e.Error(), "secret-fixture") || strings.Contains(e.Error(), "endpoint-secret")) {
				t.Error("HTTP error exposed credentials")
			}
		})
	}
}

func TestDOWAYChatResponseSizeLimit(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprint(declared), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if declared {
					w.Header().Set("Content-Length", fmt.Sprint(dowayChatResponseLimit+1))
					w.WriteHeader(200)
					return
				}
				comment := ":" + strings.Repeat("x", 64<<10) + "\n"
				for i := 0; i < 257; i++ {
					if _, e := io.WriteString(w, comment); e != nil {
						return
					}
				}
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			text, e := requestDOWAYChat(context.Background(), server.Client(), dowayChatTestConfig(server.URL), dowayChatTestMessages(), true)
			if e == nil || text != "" || !strings.Contains(e.Error(), "16 MiB") {
				t.Errorf("oversized response result: %v", e)
			}
		})
	}
}

func TestDOWAYChatEndpointAndTransportErrorsAreRedacted(t *testing.T) {
	for _, endpoint := range []string{"http://localhost", "https://", "https://secret-fixture@host.test", "https://host.test/#secret-fixture", "https://host.test/%xx-secret-fixture"} {
		text, e := requestDOWAYChat(context.Background(), http.DefaultClient, dowayChatTestConfig(endpoint), dowayChatTestMessages(), false)
		if e == nil || text != "" || strings.Contains(e.Error(), "secret-fixture") {
			t.Error("invalid endpoint accepted or exposed")
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted TLS request reached handler")
	}))
	defer server.Close()
	text, e := requestDOWAYChat(context.Background(), &http.Client{Timeout: 3 * time.Second}, dowayChatTestConfig(server.URL+"?token=secret-fixture"), dowayChatTestMessages(), false)
	if e == nil || text != "" || strings.Contains(e.Error(), "secret-fixture") {
		t.Error("TLS failure accepted or exposed endpoint credentials")
	}
}
