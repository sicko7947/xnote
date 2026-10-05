package xnote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Retain only the provider's numeric failure reason, never its freeform body.
type dowayTaskFailure struct{ FailType *int }

func (e *dowayTaskFailure) Error() string {
	if e.FailType != nil {
		return fmt.Sprintf("DOWAY transcription failed (failType %d)", *e.FailType)
	}
	return "DOWAY transcription failed"
}

// These functions consume the data field after cloudPost has checked the outer
// API response. start_asr returns XfRaTranslateInfo; get_result returns an encoded
// provider result containing content.orderInfo and content.orderResult.
func parseDOWAYStart(data json.RawMessage) (status int, orderID string, err error) {
	data, err = dowayResultJSON(data)
	if err != nil {
		return -1, "", err
	}
	var response struct {
		Status  *int            `json:"status"`
		OrderID string          `json:"orderId"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(data, &response) != nil || response.Status == nil {
		return -1, "", errors.New("DOWAY start response has no valid status")
	}
	status = *response.Status
	if err = dowayResultCode(response.Code); err != nil {
		return status, "", err
	}
	if err = dowayResultStatus(status); err != nil {
		return status, "", err
	}
	if strings.TrimSpace(response.OrderID) == "" {
		return status, "", errors.New("DOWAY start response has no order ID")
	}
	return status, response.OrderID, nil
}

// A positive readOss requests a separate download. It can arrive without content,
// in which case status is -1 and the caller must fetch OSS before checking status.
// App 3.7.7 uses status 0=create, 3=processing, 4=complete, -1=failed on this path.
func parseDOWAYPoll(data json.RawMessage) (status int, readOSS bool, orderResult string, err error) {
	return parseDOWAYPollResult(data, true)
}

// The app replaces the poll's content with the downloaded object. It does not
// re-read its readOss or code fields; the original response's code still applies.
func parseDOWAYStoredResult(data json.RawMessage) (status int, orderResult string, err error) {
	status, _, orderResult, err = parseDOWAYPollResult(data, false)
	return status, orderResult, err
}

func parseDOWAYPollResult(data json.RawMessage, followOSS bool) (status int, readOSS bool, orderResult string, err error) {
	data, err = dowayResultJSON(data)
	if err != nil {
		return -1, false, "", err
	}
	var response struct {
		Code    json.RawMessage `json:"code"`
		ReadOSS json.RawMessage `json:"readOss"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(data, &response) != nil {
		return -1, false, "", errors.New("invalid DOWAY poll response")
	}
	if followOSS {
		if err = dowayResultCode(response.Code); err != nil {
			return -1, false, "", err
		}
		var read int
		if len(response.ReadOSS) > 0 && json.Unmarshal(response.ReadOSS, &read) != nil || read < 0 {
			return -1, false, "", errors.New("invalid DOWAY poll response")
		}
		if read > 0 {
			// App 3.7.7 downloads and replaces the initial content before it
			// inspects status, even if that content is absent or malformed.
			return -1, true, "", nil
		}
	}
	var content struct {
		OrderInfo *struct {
			Status   *int `json:"status"`
			FailType *int `json:"failType"`
		} `json:"orderInfo"`
		OrderResult string `json:"orderResult"`
	}
	if json.Unmarshal(response.Content, &content) != nil || content.OrderInfo == nil || content.OrderInfo.Status == nil {
		return -1, false, "", errors.New("DOWAY poll response has no valid order status")
	}
	status = *content.OrderInfo.Status
	if status == -1 {
		return status, false, "", &dowayTaskFailure{FailType: content.OrderInfo.FailType}
	}
	if err = dowayResultStatus(status); err != nil {
		return status, false, "", err
	}
	orderResult = content.OrderResult
	if status == 4 && strings.TrimSpace(orderResult) == "" {
		return status, false, "", errors.New("DOWAY completed order has no transcript")
	}
	return status, false, orderResult, nil
}

// HTTP orderResult uses TranslateInfo.fromSimpleJson: a=src, d=bg, e=ed,
// r=speaker. The app copies bg/ed unchanged to RecordSentence.start/end, whose
// milliseconds are multiplied by 1000 for Duration before AudioPlayer.seek.
// Evidence: translate_info.dart:296-675, xf_asr_helper.dart:5394-5445,
// RecordSentence.dart:38-56, play_mgr.dart:211-225 in App 3.7.7's decompilation.
func parseDOWAYOrderResult(raw []byte) (TranscriptResult, error) {
	data, err := dowayResultJSON(raw)
	if err != nil {
		return TranscriptResult{}, err
	}
	if data[0] == '{' {
		// Cloud RecordSentence documents are supported separately. Do not feed
		// arbitrary provider objects (e.g. lattice2) to a permissive decoder.
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object["sentences"] == nil {
			return TranscriptResult{}, errors.New("unsupported DOWAY transcript object")
		}
		result, err := ParseDOWAYTranscript(data)
		if err != nil {
			return TranscriptResult{}, err
		}
		if len(result.Segments) == 0 {
			return TranscriptResult{}, errors.New("DOWAY transcript object has no sentences")
		}
		return result, nil
	}
	var rows []map[string]json.RawMessage
	if data[0] != '[' || json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
		return TranscriptResult{}, errors.New("DOWAY order result has no transcript rows")
	}
	var result TranscriptResult
	var text strings.Builder
	for i, row := range rows {
		textKey, startKey, endKey, speakerKey := "a", "d", "e", "r"
		if _, compact := row[textKey]; !compact {
			textKey, startKey, endKey, speakerKey = "src", "bg", "ed", "speaker"
		}
		var value *string
		if json.Unmarshal(row[textKey], &value) != nil || value == nil {
			return TranscriptResult{}, fmt.Errorf("DOWAY transcript row %d has no source text", i+1)
		}
		if *value == "" {
			continue
		}
		var start, end *int64
		if json.Unmarshal(row[startKey], &start) != nil || json.Unmarshal(row[endKey], &end) != nil ||
			start == nil || end == nil || *start < 0 || *end < *start {
			return TranscriptResult{}, fmt.Errorf("DOWAY transcript row %d has invalid timing", i+1)
		}
		speaker, err := dowayResultSpeaker(row[speakerKey])
		if err != nil {
			return TranscriptResult{}, fmt.Errorf("DOWAY transcript row %d has invalid speaker", i+1)
		}
		// The app concatenates source strings as received; adding a separator
		// between word or punctuation rows would corrupt the provider's text.
		text.WriteString(*value)
		result.Segments = append(result.Segments, Segment{
			Start: float64(*start) / 1000, End: float64(*end) / 1000,
			Text: *value, Speaker: speaker, Timing: "provider",
		})
	}
	result.Text = strings.TrimSpace(text.String())
	if result.Text == "" {
		return TranscriptResult{}, errors.New("DOWAY order result has no source text")
	}
	return result, nil
}

func dowayResultJSON(raw []byte) ([]byte, error) {
	data := bytes.TrimSpace(raw)
	if len(data) > 0 && data[0] == '"' {
		var encoded string
		if json.Unmarshal(data, &encoded) != nil {
			return nil, errors.New("invalid DOWAY result JSON")
		}
		data = bytes.TrimSpace([]byte(encoded))
	}
	if len(data) == 0 || !json.Valid(data) || (data[0] != '{' && data[0] != '[') {
		return nil, errors.New("invalid DOWAY result JSON")
	}
	return data, nil
}

func dowayResultStatus(status int) error {
	switch status {
	case 0, 3, 4:
		return nil
	case -1:
		return errors.New("DOWAY transcription failed")
	default:
		return fmt.Errorf("unsupported DOWAY order status %d", status)
	}
}

func dowayResultCode(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		var number json.Number
		if json.Unmarshal(raw, &number) != nil {
			return errors.New("invalid DOWAY result code")
		}
		value = number.String()
	}
	code, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return errors.New("invalid DOWAY result code")
	}
	if code != 0 {
		return fmt.Errorf("DOWAY transcription failed (code %d)", code)
	}
	return nil
}

func dowayResultSpeaker(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var speaker string
	if json.Unmarshal(raw, &speaker) == nil {
		return speaker, nil
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", err
	}
	return strconv.FormatInt(number, 10), nil
}
