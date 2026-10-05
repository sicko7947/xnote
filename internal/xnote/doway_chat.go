package xnote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const dowayChatResponseLimit = int64(16 << 20)

// Authentication and provider routing are supplied by the caller. This layer
// never discovers credentials, chooses a model, or retries a paid request.
type dowayChatConfig struct {
	Endpoint       string
	Model          string
	Headers        map[string]string
	EnableThinking *bool
	JSONOutput     bool
}

type dowayChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type dowayChatResponseFormat struct {
	Type string `json:"type"`
}

type dowayChatHTTPError struct {
	StatusCode int
}

func (e *dowayChatHTTPError) Error() string {
	return fmt.Sprintf("DOWAY chat HTTP %d; request was not retried", e.StatusCode)
}

func requestDOWAYChat(ctx context.Context, client *http.Client, c dowayChatConfig, messages []dowayChatMessage, stream bool) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("DOWAY chat requires a valid HTTPS endpoint")
	}
	if client == nil || strings.TrimSpace(c.Model) == "" || len(messages) == 0 {
		return "", errors.New("DOWAY chat requires a client, model and messages")
	}
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "" {
			return "", errors.New("DOWAY chat message has no role")
		}
	}
	var responseFormat *dowayChatResponseFormat
	if c.JSONOutput {
		responseFormat = &dowayChatResponseFormat{Type: "json_object"}
	}
	payload, e := json.Marshal(struct {
		Messages       []dowayChatMessage       `json:"messages"`
		Model          string                   `json:"model"`
		Stream         bool                     `json:"stream"`
		EnableThinking *bool                    `json:"enable_thinking,omitempty"`
		ResponseFormat *dowayChatResponseFormat `json:"response_format,omitempty"`
	}{Messages: messages, Model: c.Model, Stream: stream, EnableThinking: c.EnableThinking, ResponseFormat: responseFormat})
	if e != nil {
		return "", errors.New("DOWAY chat request could not be encoded")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
	if e != nil {
		return "", errors.New("DOWAY chat request could not be created")
	}
	// bytes.Reader normally supplies GetBody. Disable replay even on stale
	// pooled connections: a failed request may already have incurred a charge.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	for key, value := range c.Headers {
		req.Header.Set(key, value)
	}
	// Copy only client policy; retain its shared transport and connection pool.
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := requestClient.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("DOWAY chat connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &dowayChatHTTPError{StatusCode: resp.StatusCode}
	}
	if resp.ContentLength > dowayChatResponseLimit {
		return "", errors.New("DOWAY chat response exceeds 16 MiB")
	}
	limited := &io.LimitedReader{R: resp.Body, N: dowayChatResponseLimit + 1}
	var text string
	if stream {
		text, e = readDOWAYChatStream(limited)
	} else {
		text, e = readDOWAYChatResponse(limited)
	}
	if limited.N == 0 {
		return "", errors.New("DOWAY chat response exceeds 16 MiB")
	}
	if e != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", e
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("DOWAY chat returned no text")
	}
	return text, nil
}

type dowayChatContent struct {
	Content      *string         `json:"content"`
	Refusal      *string         `json:"refusal"`
	ToolCalls    json.RawMessage `json:"tool_calls"`
	FunctionCall json.RawMessage `json:"function_call"`
}

type dowayChatChoice struct {
	Index        int              `json:"index"`
	Message      dowayChatContent `json:"message"`
	Delta        dowayChatContent `json:"delta"`
	FinishReason *string          `json:"finish_reason"`
}

type dowayChatResponse struct {
	Choices []dowayChatChoice `json:"choices"`
	Error   json.RawMessage   `json:"error"`
}

func readDOWAYChatResponse(reader io.Reader) (string, error) {
	data, e := io.ReadAll(reader)
	if e != nil {
		return "", errors.New("DOWAY chat response could not be read")
	}
	var response dowayChatResponse
	if !utf8.Valid(data) || json.Unmarshal(data, &response) != nil {
		return "", errors.New("invalid DOWAY chat response")
	}
	if dowayChatHasValue(response.Error) {
		return "", errors.New("DOWAY chat provider returned an error")
	}
	if len(response.Choices) != 1 || response.Choices[0].Index != 0 {
		return "", errors.New("DOWAY chat response has no single result")
	}
	choice := response.Choices[0]
	if e = validateDOWAYChatFinish(choice.FinishReason); e != nil {
		return "", e
	}
	if e = validateDOWAYChatContent(choice.Message); e != nil {
		return "", e
	}
	if choice.Message.Content == nil {
		return "", errors.New("DOWAY chat returned no text")
	}
	return *choice.Message.Content, nil
}

func validateDOWAYChatFinish(reason *string) error {
	if reason == nil {
		return errors.New("DOWAY chat response ended without a completion marker")
	}
	switch *reason {
	case "stop":
		return nil
	case "length":
		return errors.New("DOWAY chat response was truncated by the output limit")
	case "content_filter":
		return errors.New("DOWAY chat response was blocked by the content filter")
	default:
		return errors.New("DOWAY chat response did not finish normally")
	}
}

func validateDOWAYChatContent(content dowayChatContent) error {
	if content.Refusal != nil && *content.Refusal != "" {
		return errors.New("DOWAY chat provider declined the request")
	}
	if dowayChatHasValue(content.ToolCalls) {
		var calls []json.RawMessage
		if json.Unmarshal(content.ToolCalls, &calls) != nil || len(calls) > 0 {
			return errors.New("DOWAY chat returned a tool call instead of text")
		}
	}
	if dowayChatHasValue(content.FunctionCall) {
		return errors.New("DOWAY chat returned a tool call instead of text")
	}
	return nil
}

func dowayChatHasValue(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func readDOWAYChatStream(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), int(dowayChatResponseLimit)+1)
	scanner.Split(dowaySSELine)
	var text, data strings.Builder
	event := ""
	firstLine := true
	finished, done := false, false
	dispatch := func() error {
		if event == "error" {
			return errors.New("DOWAY chat provider returned a stream error")
		}
		if data.Len() == 0 {
			return nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		if strings.TrimSpace(payload) == "[DONE]" {
			done = true
			return nil
		}
		var response dowayChatResponse
		if !utf8.ValidString(payload) || json.Unmarshal([]byte(payload), &response) != nil {
			return errors.New("invalid DOWAY chat stream event")
		}
		if dowayChatHasValue(response.Error) {
			return errors.New("DOWAY chat provider returned a stream error")
		}
		if response.Choices == nil {
			return errors.New("invalid DOWAY chat stream event")
		}
		// Usage and Azure prompt-filter chunks have an explicit empty choices
		// array. A missing/null choices field is not a valid chunk.
		if len(response.Choices) == 0 {
			return nil
		}
		if len(response.Choices) != 1 || response.Choices[0].Index != 0 {
			return errors.New("DOWAY chat stream has no single result")
		}
		choice := response.Choices[0]
		if e := validateDOWAYChatContent(choice.Delta); e != nil {
			return e
		}
		if choice.Delta.Content != nil {
			if finished && *choice.Delta.Content != "" {
				return errors.New("DOWAY chat stream contains text after completion")
			}
			text.WriteString(*choice.Delta.Content)
		}
		if choice.FinishReason != nil {
			if e := validateDOWAYChatFinish(choice.FinishReason); e != nil {
				return e
			}
			finished = true
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		if line == "" {
			if e := dispatch(); e != nil {
				return "", e
			}
			if done {
				// DONE is the protocol boundary; a server may keep its SSE body
				// open afterwards. Do not wait indefinitely for a second end.
				return text.String(), nil
			}
			data.Reset()
			event = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		case "event":
			event = value
		}
	}
	if scanner.Err() != nil {
		return "", errors.New("DOWAY chat stream could not be read completely")
	}
	// EOF does not dispatch an unfinished SSE event. Reject it even after a
	// stop marker, since the unfinished event could itself be a provider error.
	if data.Len() != 0 || event == "error" || !finished {
		return "", errors.New("DOWAY chat stream ended without complete termination")
	}
	return text.String(), nil
}

// SSE accepts LF, CRLF and bare CR. Waiting on a final CR lets a CRLF pair
// split across network reads remain one delimiter.
func dowaySSELine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' {
			return i + 1, data[:i], nil
		}
		if b == '\r' {
			if i+1 == len(data) && !atEOF {
				return 0, nil, nil
			}
			advance = i + 1
			if advance < len(data) && data[advance] == '\n' {
				advance++
			}
			return advance, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
