package xnote

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf16"
)

func fakeDOWAYTemplateProfile() (fs.FS, []byte, []byte) {
	key, iv := []byte("0123456789abcdef0123456789abcdef"), []byte("0123456789abcdef")
	data, _ := json.Marshal(map[string]string{"template_aes_key": string(key), "template_aes_iv": string(iv)})
	return fstest.MapFS{"app_profile.json": &fstest.MapFile{Data: data}}, key, iv
}

func encryptDOWAYTestTemplate(t *testing.T, text string, key, iv []byte) string {
	t.Helper()
	data := []byte(text)
	padding := aes.BlockSize - len(data)%aes.BlockSize
	for i := 0; i < padding; i++ {
		data = append(data, byte(padding))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(data, data)
	return base64.StdEncoding.EncodeToString(data)
}

func TestDOWAYSummaryDecryptsOnlyServerCredentialAndValidatesPadding(t *testing.T) {
	profiles, key, iv := fakeDOWAYTemplateProfile()
	gotKey, gotIV, err := dowayTemplateCipher(profiles)
	if err != nil || string(gotKey) != string(key) || string(gotIV) != string(iv) {
		t.Fatal("profile cipher mismatch")
	}
	encoded := encryptDOWAYTestTemplate(t, "account-issued-key", key, iv)
	plain, err := decryptDOWAYTemplate(encoded, key, iv)
	if err != nil || plain != "account-issued-key" {
		t.Fatal("server credential was not decrypted")
	}
	for _, bad := range []string{"", "not base64 account-issued-key", base64.StdEncoding.EncodeToString([]byte("short")), base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := decryptDOWAYTemplate(bad, key, iv); err == nil || strings.Contains(err.Error(), "account-issued-key") {
			t.Fatal("invalid ciphertext accepted or echoed")
		}
	}
	if _, _, err = dowayTemplateCipher(fstest.MapFS{}); err == nil {
		t.Fatal("missing decryption profile accepted")
	}
}

func TestDOWAYSummaryAccountConfigRequestAndReturnedModel(t *testing.T) {
	profiles, key, iv := fakeDOWAYTemplateProfile()
	for _, area := range []int{0, 1} {
		t.Run(strconv.Itoa(area), func(t *testing.T) {
			calls := 0
			c := Config{Serial: "TEST123", Locale: "zh-CN", SummaryLanguage: "en"}
			r := Record{Serial: "TEST123"}
			session := cloudSession{PlayerID: json.Number("9007199254740993"), Token: "test-session"}
			post := func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
				calls++
				switch path {
				case "/api/device/get_device_info":
					if !reflect.DeepEqual(body, map[string]any{"sn": "TEST123", "token": "test-session"}) {
						t.Fatal("wrong device request")
					}
					return json.RawMessage(`{"sn":"TEST123","playerId":9007199254740993,"useServerAiModel":1,"areaType":` + strconv.Itoa(area) + `}`), nil
				case "/api/player/summary_prompt_v2":
					model := "qwen3.7-plus"
					if area == 1 {
						model = "gpt-5.6-luna"
					}
					want := map[string]any{"playerId": session.PlayerID, "templateId": 2000001, "langCode": "en", "localeCode": "zh", "modelName": model, "sn": "TEST123", "token": "test-session"}
					if !reflect.DeepEqual(body, want) {
						t.Fatalf("wrong template request: %#v", body)
					}
					return json.Marshal(dowaySummaryPromptResponse{Tips: "Official plaintext template", ModelName: "returned-account-model", APIURL: "https://model.example/v1/chat/completions", APIKey: encryptDOWAYTestTemplate(t, "account-issued-key", key, iv), APIKeyHeaderName: "api-key", APIKeyHeaderPrefix: "Token "})
				default:
					t.Fatal("unexpected request")
					return nil, nil
				}
			}
			plan, err := prepareDOWAYSummary(context.Background(), c, r, session, post, profiles)
			if err != nil || calls != 2 || plan.Area != area || plan.Chat.Model != "returned-account-model" || plan.Prompt != "Official plaintext template" {
				t.Fatalf("plan contract: %+v %v", plan, err)
			}
			if plan.Chat.Headers["Api-Key"] != "Token account-issued-key" || plan.Chat.Headers["Authorization"] != "Bearer account-issued-key" || plan.Chat.Headers["Content-Type"] != "application/json" {
				t.Fatal("dynamic authentication headers differ")
			}
		})
	}
}

func TestDOWAYSummaryNoStaticModelFallback(t *testing.T) {
	profiles, key, iv := fakeDOWAYTemplateProfile()
	for _, bad := range []dowaySummaryPromptResponse{
		{ModelName: "model", APIURL: "https://example.test", APIKey: encryptDOWAYTestTemplate(t, "secret", key, iv)},
		{ModelName: "model", APIURL: "http://example.test", APIKey: "bad", APIKeyHeaderName: "Authorization"},
		{APIURL: "https://example.test", APIKey: "bad", APIKeyHeaderName: "Authorization"},
	} {
		if _, err := dowayServerChatConfig(bad, key, iv); err == nil {
			t.Fatal("invalid dynamic configuration used a fallback")
		}
	}
	calls := 0
	_, err := prepareDOWAYSummary(context.Background(), Config{Serial: "TEST", Locale: "en"}, Record{Serial: "TEST"}, cloudSession{PlayerID: json.Number("123"), Token: "test"}, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"sn":"TEST","playerId":123,"useServerAiModel":0}`), nil
	}, profiles)
	if err == nil || calls != 1 {
		t.Fatal("disabled server AI fetched a provider configuration")
	}
}

const fakeDOWAYSummaryRaw = `{"title":"AI title","keywords":["recording","summary","test"],"markdown":"## Notes\n\nConfirmed fact."}`

type dowaySummaryFixture struct {
	s              *Store
	c              Config
	r              Record
	d              dowaySummaryDeps
	chats, reports int
	report         map[string]any
}

func newDOWAYSummaryFixture(t *testing.T) *dowaySummaryFixture {
	t.Helper()
	s, rows := summaryFixture(t, 1)
	c := s.Config()
	c.Locale, c.SummaryLanguage = "en", "en"
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.path(".work/doway-session.json"), cloudSession{PlayerID: json.Number("1234567"), Token: "account-token-secret"}); err != nil {
		t.Fatal(err)
	}
	f := &dowaySummaryFixture{s: s, c: c, r: rows[0]}
	f.d = dowaySummaryDeps{
		Prepare: func(context.Context, Config, Record, cloudSession) (dowaySummaryPlan, error) {
			return dowaySummaryPlan{Chat: dowayChatConfig{Endpoint: "https://model.example/v1/chat/completions", Model: "returned-model", Headers: map[string]string{"Authorization": "Bearer provider-key-secret"}}, Prompt: "Official template", Area: 0}, nil
		},
		Chat: func(_ context.Context, c dowayChatConfig, messages []dowayChatMessage) (string, error) {
			f.chats++
			if f.job(t).Phase != "requesting" {
				t.Fatal("model request preceded durable marker")
			}
			if c.Model != "returned-model" || len(messages) != 2 || messages[1].Role != "user" || messages[1].Content != f.r.Transcript || !strings.Contains(messages[0].Content, "Do not invent") {
				t.Fatal("wrong model or source/prompt contract")
			}
			return fakeDOWAYSummaryRaw, nil
		},
		Post: func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
			f.reports++
			f.report = body
			j := f.job(t)
			if path != "/api/player/add_summary_record" || j.Phase != "reporting" || j.RawOutput == "" {
				t.Fatal("report preceded durable provider response")
			}
			return json.RawMessage(`null`), nil
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}
	return f
}

func (f *dowaySummaryFixture) jobPath() string {
	return filepath.Join(f.s.Dir(f.r), ".summary", "doway-"+summaryInputHash(f.r)+"-en.json")
}
func (f *dowaySummaryFixture) job(t *testing.T) dowaySummaryJob {
	t.Helper()
	var j dowaySummaryJob
	if err := readJSON(f.jobPath(), &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestDOWAYSummaryEndToEndCachesBeforeReportAndDoesNotRepeat(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	result, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
	if err != nil || result.Title != "AI title" || len(result.Keywords) != 3 || result.Warning != "" {
		t.Fatalf("summary: %+v %v", result, err)
	}
	want := map[string]any{"fileUid": f.r.DeviceName, "playerId": json.Number("1234567"), "filename": "AI title", "token": "account-token-secret", "sn": f.r.Serial, "lang": "en", "words": len(utf16.Encode([]rune(f.r.Transcript))), "model": "openai", "engineType": 0, "categoryType": "common", "summaryType": "summary", "templateId": 2000001, "outWords": len(utf16.Encode([]rune(fakeDOWAYSummaryRaw)))}
	if !reflect.DeepEqual(f.report, want) {
		t.Fatalf("wrong summary report: %#v", f.report)
	}
	if _, err = f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.chats != 1 || f.reports != 1 || f.job(t).Phase != "completed" {
		t.Fatal("completed model or report repeated")
	}
	data, err := os.ReadFile(f.jobPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "account-token-secret") || strings.Contains(string(data), "provider-key-secret") {
		t.Fatal("credentials persisted with summary job")
	}
}

func TestDOWAYSummaryUnknownModelRequestIsNotRepeated(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	f.d.Chat = func(context.Context, dowayChatConfig, []dowayChatMessage) (string, error) {
		f.chats++
		return "", context.DeadlineExceeded
	}
	if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown request: %v", err)
	}
	if f.chats != 1 || f.reports != 0 {
		t.Fatal("uncertain model request repeated")
	}
}

func TestDOWAYSummaryReportFailureKeepsVisibleResultWithoutReplay(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	f.d.Post = func(context.Context, string, map[string]any) (json.RawMessage, error) {
		f.reports++
		return nil, context.DeadlineExceeded
	}
	for i := 0; i < 2; i++ {
		result, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
		if err != nil || result.Title != "AI title" || result.Markdown == "" || result.Warning == "" {
			t.Fatalf("cached response not visible: %+v %v", result, err)
		}
	}
	if f.chats != 1 || f.reports != 1 || f.job(t).Phase != "reporting" {
		t.Fatal("unknown usage report replayed")
	}
}

func TestDOWAYSummaryInvalidModelJSONStillReportsUsageOnce(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	f.d.Chat = func(context.Context, dowayChatConfig, []dowayChatMessage) (string, error) {
		f.chats++
		return "A complete non-JSON response 😀", nil
	}
	for i := 0; i < 2; i++ {
		if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	if f.chats != 1 || f.reports != 1 || f.job(t).Phase != "completed" || f.report["outWords"] != 31 {
		t.Fatalf("invalid-format usage missing or replayed: chats=%d reports=%d outWords=%v", f.chats, f.reports, f.report["outWords"])
	}
	if f.report["filename"] != f.r.Title {
		t.Fatal("unparseable result changed report filename")
	}
}

func TestDOWAYSummaryExplicitHTTPRejectionAllowsManualRetryOnly(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 422, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := newDOWAYSummaryFixture(t)
			chat := f.d.Chat
			f.d.Chat = func(context.Context, dowayChatConfig, []dowayChatMessage) (string, error) {
				f.chats++
				return "", &dowayChatHTTPError{StatusCode: status}
			}
			if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err == nil {
				t.Fatal("HTTP rejection accepted")
			}
			if f.chats != 1 {
				t.Fatal("request automatically retried")
			}
			f.d.Chat = chat
			_, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
			if status == 500 {
				if err == nil || f.chats != 1 {
					t.Fatal("ambiguous server failure repeated")
				}
			} else if err != nil || f.chats != 2 || f.reports != 1 {
				t.Fatalf("explicit manual retry failed: %v", err)
			}
		})
	}
}

func TestDOWAYSummaryUsesCloudThenExistingDOWAYIdentity(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing_job", true: "cloud"}[cloud], func(t *testing.T) {
			f := newDOWAYSummaryFixture(t)
			j := dowayJob{Version: 1, UID: "xnote_existing", RecordID: f.r.ID, PlayerID: "1234567", Serial: f.r.Serial, Duration: 7, Preflight: dowayPreflight{Area: 0, Bucket: "chinaxnote"}, ResultBucket: "chinaxnote"}
			j.UploadKey, _ = dowayAudioObjectKey(j.Serial, j.PlayerID, j.UID)
			j.ResultKey, _ = dowayTranscriptObjectKey(j.PlayerID, j.UID)
			if err := atomicJSON(filepath.Join(f.s.Dir(f.r), ".transcription", "doway-job.json"), j); err != nil {
				t.Fatal(err)
			}
			want := j.UID
			if cloud {
				f.r.CloudUID = "cloud-record-identity"
				want = f.r.CloudUID
			}
			if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err != nil {
				t.Fatal(err)
			}
			if f.report["fileUid"] != want {
				t.Fatal("existing recording identity not reused")
			}
		})
	}
}

func TestDOWAYSummaryThinkingOnlyForVerifiedDashScopeModels(t *testing.T) {
	profiles, key, iv := fakeDOWAYTemplateProfile()
	for _, tc := range []struct {
		host, model string
		enabled     bool
	}{
		{"dashscope.aliyuncs.com", "qwen3.5-plus", true},
		{"dashscope.aliyuncs.com", "qwen3.5-plus-2026-02-15", true},
		{"dashscope.aliyuncs.com", "qwen3.5-plus-unknown-version", false},
		{"model.example", "qwen3.5-plus", false},
		{"dashscope.aliyuncs.com", "other-model", false},
	} {
		for _, thinking := range []bool{false, true} {
			t.Run(tc.host+"/"+tc.model+"/"+strconv.FormatBool(thinking), func(t *testing.T) {
				c := Config{Serial: "TEST", Locale: "en", SummaryThinking: thinking}
				plan, err := prepareDOWAYSummary(context.Background(), c, Record{Serial: "TEST"}, cloudSession{PlayerID: json.Number("123"), Token: "test"}, func(_ context.Context, path string, _ map[string]any) (json.RawMessage, error) {
					if path == "/api/device/get_device_info" {
						return json.RawMessage(`{"sn":"TEST","playerId":123,"areaType":0,"useServerAiModel":1}`), nil
					}
					return json.Marshal(dowaySummaryPromptResponse{Tips: "Template", ModelName: tc.model, APIURL: "https://" + tc.host + "/chat", APIKey: encryptDOWAYTestTemplate(t, "account-key", key, iv), APIKeyHeaderName: "Authorization", APIKeyHeaderPrefix: "Bearer "})
				}, profiles)
				if err != nil {
					t.Fatal(err)
				}
				if plan.Chat.JSONOutput != tc.enabled {
					t.Fatal("JSON output mode was not limited to verified DashScope models")
				}
				if tc.enabled {
					if plan.Chat.EnableThinking == nil || *plan.Chat.EnableThinking != thinking {
						t.Fatal("verified model missing explicit thinking setting")
					}
				} else if plan.Chat.EnableThinking != nil {
					t.Fatal("unknown model received unverified thinking parameter")
				}
			})
		}
	}
}

func TestDOWAYSummaryThinkingChangeDoesNotReplayAnUnknownRequest(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	f.d.Chat = func(context.Context, dowayChatConfig, []dowayChatMessage) (string, error) {
		f.chats++
		return "", context.DeadlineExceeded
	}
	if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err == nil {
		t.Fatal("unknown request accepted")
	}
	f.c.SummaryThinking = true
	if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err == nil || !strings.Contains(err.Error(), "thinking setting") {
		t.Fatalf("changed mode: %v", err)
	}
	if f.chats != 1 || f.reports != 0 {
		t.Fatal("mode change repeated uncertain request")
	}
}

func TestDOWAYSummaryCompletedCacheSurvivesThinkingChange(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	f.c.SummaryThinking = true
	if result, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err != nil || result.Title != "AI title" {
		t.Fatal("completed result not reused")
	}
	if f.chats != 1 || f.reports != 1 {
		t.Fatal("thinking setting regenerated completed output")
	}
}

func TestDOWAYSummaryReportPreservesRenameDuringModelRequest(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	chat := f.d.Chat
	f.d.Chat = func(ctx context.Context, c dowayChatConfig, messages []dowayChatMessage) (string, error) {
		if err := f.s.Update(f.r.ID, func(r *Record) { r.Title, r.TitleSource = "Current manual title", "local" }); err != nil {
			t.Fatal(err)
		}
		return chat(ctx, c, messages)
	}
	if _, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.report["filename"] != "Current manual title" {
		t.Fatal("usage report replaced the latest manual title")
	}
}

const fakeDOWAYSummaryUnescapedRaw = `{"title":"Quoted notes","keywords":["recording","summary","test"],"markdown":"## Notes\nThey said "ready" and kept \"escaped quotes\".\nPath C:\\notes; Unicode \u4e2d\u6587.
A literal newline remains."}`

func TestDOWAYSummaryRepairsOnlyFinalMarkdownString(t *testing.T) {
	want := "## Notes\nThey said \"ready\" and kept \"escaped quotes\".\nPath C:\\notes; Unicode 中文.\nA literal newline remains."
	for _, raw := range []string{fakeDOWAYSummaryUnescapedRaw, "```json\n" + fakeDOWAYSummaryUnescapedRaw + "\n```"} {
		got, err := parseDOWAYSummaryResult(raw)
		if err != nil || got.Title != "Quoted notes" || len(got.Keywords) != 3 || got.Markdown != want {
			t.Fatalf("complete Markdown recovery: %#v %v", got, err)
		}
	}
}

func TestDOWAYSummaryRepairRejectsMalformedPrefixOrEnding(t *testing.T) {
	for name, raw := range map[string]string{
		"missing title":     `{"keywords":["test"],"markdown":"A "quote"."}`,
		"empty title":       `{"title":"","keywords":["test"],"markdown":"A "quote"."}`,
		"invalid title":     `{"title":"A "bad" title","keywords":["test"],"markdown":"A "quote"."}`,
		"duplicate title":   `{"title":"one","title":"two","markdown":"A "quote"."}`,
		"invalid keywords":  `{"title":"Title","keywords":["bad "quote""],"markdown":"A "quote"."}`,
		"empty keywords":    `{"title":"Title","keywords":[],"markdown":"A "quote"."}`,
		"missing brace":     `{"title":"Title","keywords":["test"],"markdown":"A "quote"."`,
		"missing end quote": `{"title":"Title","keywords":["test"],"markdown":"A "quote".}`,
		"unfinished escape": `{"title":"Title","keywords":["test"],"markdown":"A "quote".\"}`,
		"invalid escape":    `{"title":"Title","keywords":["test"],"markdown":"A "quote".\q"}`,
		"trailing member":   `{"title":"Title","keywords":["test"],"markdown":"A "quote".","extra":"do not swallow"}`,
		"unquoted member":   `{"title":"Title","keywords":["test"],"markdown":"A "quote".",extra:"do not swallow"}`,
		"malformed member":  `{"title":"Title","keywords":["test"],"markdown":"A "quote".","extra":broken "tail"}`,
		"trailing object":   `{"title":"Title","keywords":["test"],"markdown":"A "quote"."} {"extra":"do not swallow"}`,
		"trailing text":     `{"title":"Title","keywords":["test"],"markdown":"A "quote"."} trailing`,
		"not last field":    `{"title":"Title","markdown":"A "quote".","keywords":["test"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDOWAYSummaryResult(raw); err == nil {
				t.Fatal("malformed structure or truncated response was repaired")
			}
		})
	}
}

func TestDOWAYSummaryCompletedInvalidCacheRecoversWithoutRequests(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	job := dowaySummaryJob{Version: 1, RecordID: f.r.ID, PlayerID: "1234567", Serial: f.r.Serial, FileUID: f.r.DeviceName, SourceHash: summaryInputHash(f.r), Language: "en", Phase: "completed", RawOutput: fakeDOWAYSummaryUnescapedRaw, Words: 12, OutWords: 42}
	if err := saveDOWAYSummaryJob(f.jobPath(), &job, f.d.Now); err != nil {
		t.Fatal(err)
	}
	f.d.Prepare = func(context.Context, Config, Record, cloudSession) (dowaySummaryPlan, error) {
		t.Fatal("cached response must not acquire another model configuration")
		return dowaySummaryPlan{}, nil
	}
	f.d.Chat = func(context.Context, dowayChatConfig, []dowayChatMessage) (string, error) {
		t.Fatal("cached response must not call the model again")
		return "", nil
	}
	f.d.Post = func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("completed usage must not be reported again")
		return nil, nil
	}
	for i := 0; i < 2; i++ {
		result, err := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
		if err != nil || result.Title != "Quoted notes" || !strings.Contains(result.Markdown, `They said "ready"`) {
			t.Fatalf("cached response not recovered: %#v %v", result, err)
		}
	}
	saved := f.job(t)
	if saved.Phase != "completed" || saved.Result == nil || saved.RawOutput != job.RawOutput || saved.Words != job.Words || saved.OutWords != job.OutWords {
		t.Fatal("recovery failed to persist the result or modified raw output/accounting")
	}
}
