package xnote

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestPrepareDOWAYTranscriptionContract(t *testing.T) {
	// Above 2^53: the account identity must never round through float64.
	session := cloudSession{PlayerID: json.Number("9007199254740993"), Token: "test-token"}
	c := Config{Serial: "RECORDER", Language: "zh-CN"}
	r := Record{Serial: c.Serial}
	calls := 0
	p, err := prepareDOWAYTranscription(context.Background(), c, r, session, func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
		calls++
		switch path {
		case "/api/device/get_device_info":
			if !reflect.DeepEqual(body, map[string]any{"sn": c.Serial, "token": session.Token}) {
				t.Fatal("incorrect device-info request")
			}
			return json.RawMessage(`{"sn":"RECORDER","playerId":9007199254740993,"areaType":0,"chargePlan":2,"asrEnable":0,"duration":0,"starterDuration":36000}`), nil
		default:
			t.Fatalf("unexpected request %s", path)
			return nil, nil
		}
	})
	want := dowayPreflight{Language: "cn", LanguageID: "1", VerifyLanguageCode: "en", ReportLanguage: "zh-Hans", ReportLanguageCode: "zh_cn", Area: 0, Bucket: "chinaxnote", Unlimited: 1}
	if err != nil || p != want || calls != 1 {
		t.Fatalf("preflight=%+v calls=%d err=%v", p, calls, err)
	}
}

func TestPrepareDOWAYTranscriptionLanguagesAndRegions(t *testing.T) {
	for _, tt := range []struct {
		name, language, languageID, startLanguage, region, area, bucket string
	}{
		{"English US", "en-US", "3", "en", "us-east-1", "1", "useastxnote"},
		{"Japanese", "ja", "4", "ja", "eu-central-1", "1", "frankxnote"},
		{"Korean", "ko-KR", "5", "ko", "", "1", "asiaxnote"},
		{"French", "fr-FR", "6", "fr", "", "1", "asiaxnote"},
		{"Spanish", "es", "7", "es", "", "1", "asiaxnote"},
		{"Russian", "ru", "9", "ru", "", "1", "asiaxnote"},
		{"German", "de", "11", "de", "", "1", "asiaxnote"},
		{"Italian", "it", "12", "it", "", "1", "asiaxnote"},
		{"Vietnamese", "vi", "22", "vi", "", "1", "asiaxnote"},
		{"Arabic", "ar", "31", "ar", "", "1", "asiaxnote"},
		{"Chinese default region", "zh", "1", "cn", "", "1", "asiaxnote"},
		{"unknown area defaults overseas", "en", "3", "en", "ap-southeast-1", "2", "asiaxnote"},
		{"missing area defaults overseas", "en", "3", "en", "new-region", "null", "asiaxnote"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Serial: "RECORDER", Language: tt.language}
			session := cloudSession{PlayerID: "42", Token: "test-token"}
			p, err := prepareDOWAYTranscription(context.Background(), c, Record{Serial: c.Serial}, session, func(_ context.Context, path string, _ map[string]any) (json.RawMessage, error) {
				if path == "/api/device/get_device_info" {
					return json.RawMessage(`{"sn":"RECORDER","playerId":42,"areaType":` + tt.area + `,"chargePlan":0}`), nil
				}
				return json.RawMessage(`{"playerId":42,"awsRegion":"` + tt.region + `"}`), nil
			})
			if err != nil || p.Language != tt.startLanguage || p.LanguageID != tt.languageID || p.Bucket != tt.bucket || p.Area != 1 || p.Unlimited != 1 {
				t.Fatalf("preflight=%+v err=%v", p, err)
			}
			if tt.region == "" && p.Region != "ap-southeast-1" {
				t.Fatalf("default region=%q", p.Region)
			}
		})
	}
}

func TestDOWAYCompletionLanguageCodesFollowAppModels(t *testing.T) {
	for _, tt := range []struct{ input, lang, code string }{
		{"zh", "zh-Hans", "zh_cn"}, {"en", "en", "en_us"},
		{"ja", "ja", "ja_jp"}, {"ko", "ko", "ko_kr"},
		{"fr", "fr", "fr_fr"}, {"es", "es", "es_es"},
		{"ru", "ru", "ru_ru"}, {"de", "de", "de_DE"},
		{"it", "it", "it_IT"}, {"vi", "vi", "vi_VN"}, {"ar", "ar", "ar_il"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			p, err := dowayLanguagePreflight(tt.input)
			if err != nil || p.ReportLanguage != tt.lang || p.ReportLanguageCode != tt.code {
				t.Fatalf("report lang=%q code=%q err=%v", p.ReportLanguage, p.ReportLanguageCode, err)
			}
		})
	}
}

func TestPrepareDOWAYTranscriptionRejectsBeforeRequests(t *testing.T) {
	for _, tt := range []struct {
		name     string
		config   Config
		record   Record
		session  cloudSession
		canceled bool
	}{
		{"auto language", Config{Serial: "SN", Language: "auto"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, false},
		{"empty language", Config{Serial: "SN"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, false},
		{"unsupported language", Config{Serial: "SN", Language: "und"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, false},
		{"different recorder", Config{Serial: "SN", Language: "en"}, Record{Serial: "OTHER"}, cloudSession{"42", "token"}, false},
		{"missing recorder", Config{Language: "en"}, Record{}, cloudSession{"42", "token"}, false},
		{"invalid account", Config{Serial: "SN", Language: "en"}, Record{Serial: "SN"}, cloudSession{"1.5", "token"}, false},
		{"missing token", Config{Serial: "SN", Language: "en"}, Record{Serial: "SN"}, cloudSession{"42", ""}, false},
		{"canceled", Config{Serial: "SN", Language: "en"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			_, err := prepareDOWAYTranscription(ctx, tt.config, tt.record, tt.session, func(context.Context, string, map[string]any) (json.RawMessage, error) {
				t.Fatal("invalid input issued a network request")
				return nil, nil
			})
			if err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestPrepareDOWAYTranscriptionPollRegionFollowsStorageBackend(t *testing.T) {
	for _, tt := range []struct {
		name, area, aws, want string
	}{
		{"overseas AWS", "1", "1", "eu-central-1"},
		{"overseas Aliyun", "1", "0", ""},
		{"China ignores AWS flag", "0", "1", ""},
		{"missing AWS flag", "1", "null", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := prepareDOWAYTranscription(context.Background(), Config{Serial: "SN", Language: "en"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, func(_ context.Context, path string, _ map[string]any) (json.RawMessage, error) {
				if path == "/api/device/get_device_info" {
					return json.RawMessage(`{"sn":"SN","playerId":42,"areaType":` + tt.area + `,"useAwsS1":` + tt.aws + `}`), nil
				}
				return json.RawMessage(`{"playerId":42,"awsRegion":"eu-central-1"}`), nil
			})
			wantRegion := "eu-central-1"
			if tt.area == "0" {
				wantRegion = ""
			}
			if err != nil || p.PollRegion != tt.want || p.Region != wantRegion {
				t.Fatalf("preflight=%+v err=%v", p, err)
			}
		})
	}
}

func TestPrepareDOWAYTranscriptionRejectsInvalidMetadata(t *testing.T) {
	for _, tt := range []struct{ name, device, account string }{
		{"device null", `null`, `{"playerId":42}`},
		{"device missing identity", `{}`, `{"playerId":42}`},
		{"different device", `{"sn":"OTHER","playerId":42}`, `{"playerId":42}`},
		{"different owner", `{"sn":"SN","playerId":43}`, `{"playerId":42}`},
		{"area wrong type", `{"sn":"SN","playerId":42,"areaType":"0"}`, `{"playerId":42}`},
		{"device malformed", `{`, `{"playerId":42}`},
		{"account null", `{"sn":"SN","playerId":42}`, `null`},
		{"account mismatch", `{"sn":"SN","playerId":42}`, `{"playerId":43}`},
		{"account malformed", `{"sn":"SN","playerId":42}`, `{`},
		{"region wrong type", `{"sn":"SN","playerId":42}`, `{"playerId":42,"awsRegion":1}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			_, err := prepareDOWAYTranscription(context.Background(), Config{Serial: "SN", Language: "en"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, func(_ context.Context, path string, _ map[string]any) (json.RawMessage, error) {
				calls++
				if path == "/api/device/get_device_info" {
					return json.RawMessage(tt.device), nil
				}
				if path != "/api/player/get" {
					t.Fatalf("unexpected request %s", path)
				}
				return json.RawMessage(tt.account), nil
			})
			if err == nil || calls > 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestVerifyDOWAYTranscriptionContractAndNoRetry(t *testing.T) {
	p, err := dowayLanguagePreflight("zh")
	if err != nil {
		t.Fatal(err)
	}
	requestFailure := errors.New("response lost after request")
	for _, tt := range []struct {
		name string
		data json.RawMessage
		err  error
	}{
		{"successful null data", json.RawMessage(`null`), nil},
		{"successful empty data", nil, nil},
		{"successful object data", json.RawMessage(`{"unused":true}`), nil},
		{"request failure", nil, requestFailure},
		{"canceled during request", nil, context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := verifyDOWAYTranscription(context.Background(), p, Config{Serial: "SN", Language: "zh"}, Record{Serial: "SN"}, cloudSession{"9007199254740993", "token"}, 3671, func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
				calls++
				want := map[string]any{"sn": "SN", "playerId": json.Number("9007199254740993"), "token": "token", "time": int64(3671), "langID": "1", "langCode": "en", "unlimited": 1}
				if path != "/api/device/verify_device" || !reflect.DeepEqual(body, want) {
					t.Fatal("incorrect verification request")
				}
				return tt.data, tt.err
			})
			if err != tt.err || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestVerifyDOWAYTranscriptionInvalidInputDoesNotSend(t *testing.T) {
	valid, err := dowayLanguagePreflight("en")
	if err != nil {
		t.Fatal(err)
	}
	wrongLanguage := valid
	wrongLanguage.LanguageID = "1"
	wrongVerifyLanguage := valid
	wrongVerifyLanguage.VerifyLanguageCode = "cn"
	for _, tt := range []struct {
		name     string
		p        dowayPreflight
		duration int64
		canceled bool
	}{
		{"zero duration", valid, 0, false},
		{"negative duration", valid, -1, false},
		{"wrong language", wrongLanguage, 10, false},
		{"wrong verification language", wrongVerifyLanguage, 10, false},
		{"empty preflight", dowayPreflight{}, 10, false},
		{"canceled", valid, 10, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			err := verifyDOWAYTranscription(ctx, tt.p, Config{Serial: "SN"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, tt.duration, func(context.Context, string, map[string]any) (json.RawMessage, error) {
				t.Fatal("invalid input issued a verification request")
				return nil, nil
			})
			if err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestPrepareDOWAYTranscriptionCancellationStopsBeforeAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := prepareDOWAYTranscription(ctx, Config{Serial: "SN", Language: "en"}, Record{Serial: "SN"}, cloudSession{"42", "token"}, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		calls++
		cancel()
		return json.RawMessage(`{"sn":"SN","playerId":42}`), nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
