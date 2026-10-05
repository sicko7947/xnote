package xnote

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseDOWAYFailureRetainsOnlyNumericFailType(t *testing.T) {
	for _, stored := range []bool{false, true} {
		raw := json.RawMessage(`{"code":"000000","descInfo":"private-token-description","content":{"orderInfo":{"status":-1,"failType":2}}}`)
		var err error
		if stored {
			_, _, err = parseDOWAYStoredResult(raw)
		} else {
			_, _, _, err = parseDOWAYPoll(raw)
		}
		var failed *dowayTaskFailure
		if !errors.As(err, &failed) || failed.FailType == nil || *failed.FailType != 2 || err.Error() != "DOWAY transcription failed (failType 2)" {
			t.Fatalf("wrong failure detail: %v", err)
		}
	}
	_, _, _, err := parseDOWAYPoll([]byte(`{"content":{"orderInfo":{"status":-1,"failType":"private-token"}}}`))
	var failed *dowayTaskFailure
	if err == nil || errors.As(err, &failed) || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("invalid failure data accepted: %v", err)
	}
}

func TestParseDOWAYStart(t *testing.T) {
	for _, raw := range []string{
		`{"status":0,"orderId":"900719925474099312345","code":0}`,
		`{"status":3,"orderId":"900719925474099312345","code":"000000"}`,
	} {
		encoded, _ := json.Marshal(raw)
		for _, data := range [][]byte{[]byte(raw), encoded} {
			status, orderID, err := parseDOWAYStart(data)
			if err != nil || (status != 0 && status != 3) || orderID != "900719925474099312345" {
				t.Fatalf("unexpected start result: %d %q %v", status, orderID, err)
			}
		}
	}
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"status":0}`, `{"status":"0","orderId":"task"}`,
		`{"status":0,"orderId":900719925474099312345}`, `{"status":1,"orderId":"task"}`,
		`{"status":-1,"orderId":"task","code":2,"errMsg":"token=private"}`,
		`{"status":0,"orderId":"task","code":500}`, `{"status":0,"orderId":"task","code":true}`,
	} {
		_, _, err := parseDOWAYStart([]byte(raw))
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe acceptance/error for %s: %v", raw, err)
		}
	}
}

func TestParseDOWAYPollStatesAndOSS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		status int
		oss    bool
		result string
	}{
		{"created", `{"code":"000000","content":{"orderInfo":{"status":0}}}`, 0, false, ""},
		{"processing", `{"code":"000000","content":{"orderInfo":{"status":3},"orderResult":""}}`, 3, false, ""},
		{"complete", `{"code":"000000","content":{"orderInfo":{"status":4},"orderResult":"[{\"a\":\"ok\",\"d\":0,\"e\":1000}]"}}`, 4, false, `[{"a":"ok","d":0,"e":1000}]`},
		{"oss_only", `{"code":"000000","readOss":1}`, -1, true, ""},
		{"oss_complete", `{"code":0,"readOss":2,"content":{"orderInfo":{"status":4}}}`, -1, true, ""},
		{"oss_failed_initial_status", `{"code":0,"readOss":1,"content":{"orderInfo":{"status":-1}}}`, -1, true, ""},
		{"oss_unknown_initial_status", `{"code":0,"readOss":1,"content":{"orderInfo":{"status":99}}}`, -1, true, ""},
		{"oss_malformed_initial_content", `{"code":0,"readOss":1,"content":{"orderInfo":{"status":"unknown"},"orderResult":[]}}`, -1, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(tc.raw)
			for _, data := range [][]byte{[]byte(tc.raw), encoded} {
				status, oss, result, err := parseDOWAYPoll(data)
				if err != nil || status != tc.status || oss != tc.oss || result != tc.result {
					t.Fatalf("poll: status=%d oss=%v result=%q err=%v", status, oss, result, err)
				}
			}
		})
	}
	for _, raw := range []string{
		`{}`, `{"code":"000000"}`, `{"readOss":true}`, `{"readOss":-1}`,
		`{"readOss":1,"code":"123456"}`, `{"content":{"orderInfo":{"status":1}}}`,
		`{"content":{"orderInfo":{"status":2}}}`, `{"content":{"orderInfo":{"status":-1}}}`,
		`{"content":{"orderInfo":{"status":4}}}`, `{"content":{"orderInfo":{"status":4},"orderResult":[]}}`,
		`{"code":"failed","descInfo":"token=private","content":null}`,
	} {
		_, _, _, err := parseDOWAYPoll([]byte(raw))
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe acceptance/error for %s: %v", raw, err)
		}
	}
}

func TestParseDOWAYStoredResultUsesDownloadedContent(t *testing.T) {
	for _, raw := range []string{
		`{"content":{"orderInfo":{"status":4},"orderResult":"[{\"a\":\"done\",\"d\":0,\"e\":1000}]"}}`,
		`{"code":500,"readOss":1,"content":{"orderInfo":{"status":4},"orderResult":"[{\"a\":\"done\",\"d\":0,\"e\":1000}]"}}`,
		`{"code":{},"readOss":"ignored","content":{"orderInfo":{"status":4},"orderResult":"[{\"a\":\"done\",\"d\":0,\"e\":1000}]"}}`,
	} {
		status, rawResult, err := parseDOWAYStoredResult([]byte(raw))
		if err != nil || status != 4 {
			t.Fatalf("stored result: %d %q %v", status, rawResult, err)
		}
		result, err := parseDOWAYOrderResult([]byte(rawResult))
		if err != nil || result.Text != "done" || result.Segments[0].End != 1 {
			t.Fatalf("stored transcript: %+v %v", result, err)
		}
	}
	for _, raw := range []string{
		`{"readOss":1}`, `{"readOss":1,"content":{"orderInfo":{"status":-1}}}`,
		`{"content":{"orderInfo":{"status":4},"orderResult":""}}`,
	} {
		if _, _, err := parseDOWAYStoredResult([]byte(raw)); err == nil {
			t.Fatalf("stored object accepted without completed content: %s", raw)
		}
	}
}

func TestParseDOWAYOrderResult(t *testing.T) {
	for _, raw := range []string{
		`[{"a":"Hello ","b":"翻译","c":0,"d":1250,"e":2000,"f":0,"r":2,"w":0},{"a":"world.","d":2000,"e":3250,"r":2}]`,
		`[{"src":"Hello ","bg":1250,"ed":2000,"speaker":"2"},{"src":"world.","bg":2000,"ed":3250,"speaker":2}]`,
	} {
		encoded, _ := json.Marshal(raw)
		for _, data := range [][]byte{[]byte(raw), encoded} {
			result, err := parseDOWAYOrderResult(data)
			if err != nil || result.Text != "Hello world." || len(result.Segments) != 2 {
				t.Fatalf("transcript: %+v %v", result, err)
			}
			first, last := result.Segments[0], result.Segments[1]
			if first.Start != 1.25 || last.End != 3.25 || first.Speaker != "2" || first.Timing != "provider" {
				t.Fatalf("wrong timeline/speaker: %+v", result.Segments)
			}
		}
	}
	result, err := parseDOWAYOrderResult([]byte(`{"sentences":[{"start":500,"end":2500,"text":"Sentence","speaker":0}]}`))
	if err != nil || result.Text != "Sentence" || result.Segments[0].Start != .5 {
		t.Fatalf("sentence compatibility: %+v %v", result, err)
	}
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"content":"unverified object"}`, `{"lattice2":[{"text":"unsupported"}]}`,
		`{"content":"metadata only","sentences":[]}`, `{"content":"metadata only","sentences":null}`,
		`[{"a":"text"}]`, `[{"a":"text","d":-1,"e":1}]`, `[{"a":"text","d":2,"e":1}]`,
		`[{"a":"text","d":0.5,"e":1}]`, `[{"a":"text","d":"0","e":1}]`,
		`[{"a":"text","d":0,"e":1,"r":{}}]`, `[{"a":null,"d":0,"e":1}]`,
		`[{"a":""}]`, `[{"b":"translation only","d":0,"e":1}]`,
		`[{"a":"valid","d":0,"e":1},{"unknown":"partial data"}]`,
	} {
		result, err := parseDOWAYOrderResult([]byte(raw))
		if err == nil || result.Text != "" || len(result.Segments) != 0 {
			t.Fatalf("accepted malformed/partial transcript: %s %+v %v", raw, result, err)
		}
	}
}
