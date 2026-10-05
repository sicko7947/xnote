package xnote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type dowayWorkflowFixture struct {
	s                                               *Store
	c                                               Config
	r                                               Record
	d                                               dowayTranscribeDeps
	verify, upload, starts, polls, reports, deletes int
	startBody, pollBody, reportBody                 map[string]any
}

func newDOWAYWorkflowFixture(t *testing.T) *dowayWorkflowFixture {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicJSON(s.path(".work/doway-session.json"), cloudSession{PlayerID: json.Number("9007199254740993"), Token: "test-session"}); err != nil {
		t.Fatal(err)
	}
	f := &dowayWorkflowFixture{s: s, c: Config{Serial: "TEST123", Language: "en", Provider: "doway", DOWAYPublicUpload: true}, r: Record{ID: "test-record", Serial: "TEST123", DeviceName: "sample.mp3", Audio: "/fake/audio.mp3"}}
	f.d = dowayTranscribeDeps{
		Prepare: func(context.Context, Config, Record, cloudSession, func(context.Context, string, map[string]any) (json.RawMessage, error)) (dowayPreflight, error) {
			return dowayPreflight{Language: "en", LanguageID: "3", VerifyLanguageCode: "en", Area: 1, Region: "us-east-1", Bucket: "useastxnote", Unlimited: 1}, nil
		},
		Verify: func(context.Context, dowayPreflight, Config, Record, cloudSession, int64, func(context.Context, string, map[string]any) (json.RawMessage, error)) error {
			f.verify++
			if phase := f.job(t).Phase; phase != "verifying" {
				t.Fatalf("verify sent before durable marker: %s", phase)
			}
			return nil
		},
		Inspect: func(context.Context, string) (dowayAudioInfo, error) {
			return dowayAudioInfo{SHA256: "audio-sha256", Size: 58220, Duration: 7}, nil
		},
		Upload: func(context.Context, string, string, string) error { f.upload++; return nil },
		Exists: func(context.Context, string, string) (bool, error) { return false, nil },
		Read:   func(context.Context, string, string) ([]byte, error) { return nil, errors.New("unexpected OSS read") },
		Delete: func(_ context.Context, bucket, key string) error {
			j := f.job(t)
			if (bucket != j.Preflight.Bucket || key != j.UploadKey) && (bucket != j.ResultBucket || key != j.ResultKey) {
				t.Fatal("cleanup addressed an unrelated object")
			}
			f.deletes++
			return nil
		},
		Wait:   func(context.Context, time.Duration) error { return errors.New("unexpected wait") },
		Now:    func() time.Time { return time.Unix(1700000000, 0) },
		NewUID: func() (string, error) { return "xnote_test", nil },
	}
	f.d.Post = func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
		switch path {
		case "/api/audio/start_asr":
			f.starts++
			f.startBody = body
			if phase := f.job(t).Phase; phase != "submitting" {
				t.Fatalf("start sent before durable marker: %s", phase)
			}
			return json.RawMessage(`{"status":0,"orderId":"test-order","code":0}`), nil
		case "/api/audio/get_result":
			f.polls++
			f.pollBody = body
			return dowayCompletedFixture(), nil
		case "/api/player/add_transcription_record":
			f.reports++
			f.reportBody = body
			if j := f.job(t); j.Phase != "reporting" || j.ReportState != "submitting" {
				t.Fatal("report sent before durable marker")
			}
			var cached TranscriptResult
			if err := readJSON(filepath.Join(f.s.Dir(f.r), ".transcription", "doway-result.json"), &cached); err != nil || cached.Text == "" {
				t.Fatal("report sent before durable transcript")
			}
			return json.RawMessage(`null`), nil
		default:
			return nil, errors.New("unexpected endpoint")
		}
	}
	return f
}

func dowayCompletedFixture() json.RawMessage {
	return json.RawMessage(`{"code":"0","content":{"orderInfo":{"status":4},"orderResult":"[{\"a\":\"Hello there.\",\"d\":100,\"e\":7100,\"r\":1}]"}}`)
}

func (f *dowayWorkflowFixture) job(t *testing.T) dowayJob {
	t.Helper()
	var job dowayJob
	if err := readJSON(filepath.Join(f.s.Dir(f.r), ".transcription", "doway-job.json"), &job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestDOWAYWorkflowRequestContractAndCompletedCache(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	result, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Hello there." || len(result.Segments) != 1 || result.Segments[0].Start != .1 || result.Segments[0].End != 7.1 {
		t.Fatalf("wrong transcript: %+v", result)
	}
	j := f.job(t)
	wantStart := map[string]any{"fileName": "TEST1239007199254740993xnote_test.mp3", "playerId": json.Number("9007199254740993"), "fileSize": int64(58220), "duration": int64(7), "language": "en", "fileUid": "xnote_test", "type": 1, "token": "test-session", "roleType": 0, "engRLang": 0, "fileKey": "trans00000/9007199254740993_xnote_test_trans.json", "version": 2, "buckName": "useastxnote"}
	if !reflect.DeepEqual(f.startBody, wantStart) {
		t.Errorf("start request differs: got %#v", f.startBody)
	}
	wantPoll := map[string]any{"playerId": json.Number("9007199254740993"), "orderId": "test-order", "fileUid": "xnote_test", "type": 1, "token": "test-session", "fileName": j.UploadKey, "version": 2, "bucket": "asiaxnote", "region": "", "fileKey": j.ResultKey}
	if !reflect.DeepEqual(f.pollBody, wantPoll) {
		t.Errorf("poll request differs: got %#v", f.pollBody)
	}
	wantReport := map[string]any{"fileUid": "xnote_test", "playerId": json.Number("9007199254740993"), "filename": "sample.mp3", "duration": int64(7), "token": "test-session", "sn": "TEST123", "lang": "en", "words": 12, "type": 1, "model": "aliyun", "engineType": 0, "categoryTYpe": "all", "summaryType": "custom", "templateId": 0, "langID": "3", "langCode": "en_us", "unlimited": 1, "costTime": 0}
	if !reflect.DeepEqual(f.reportBody, wantReport) {
		t.Errorf("completion report differs: got %#v", f.reportBody)
	}
	if j.Phase != "completed" || j.ResultBucket != "asiaxnote" {
		t.Fatalf("job = %+v", j)
	}
	if _, err = f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.verify != 1 || f.upload != 1 || f.starts != 1 || f.polls != 1 || f.reports != 1 || f.deletes != 2 {
		t.Fatalf("completed task repeated requests: %+v", f)
	}
	b, err := os.ReadFile(filepath.Join(f.s.Dir(f.r), ".transcription", "doway-job.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "test-session") {
		t.Fatal("session token leaked to persisted job")
	}
}

func TestDOWAYWorkflowAmbiguousStartQueriesSameJobWithoutResubmit(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	post := f.d.Post
	f.d.Post = func(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
		if path == "/api/audio/start_asr" {
			f.starts++
			return nil, context.Canceled
		}
		return post(ctx, path, body)
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if f.job(t).Phase != "submitting" {
		t.Fatal("ambiguous submission marker lost")
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.verify != 1 || f.upload != 1 || f.starts != 1 || f.polls != 1 {
		t.Fatal("ambiguous submission was repeated")
	}
	if f.pollBody["fileUid"] != "xnote_test" || f.pollBody["orderId"] != "" {
		t.Fatal("resume did not query original UID")
	}
}

func TestDOWAYWorkflowAmbiguousVerificationNeverRepeated(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	f.d.Verify = func(context.Context, dowayPreflight, Config, Record, cloudSession, int64, func(context.Context, string, map[string]any) (json.RawMessage, error)) error {
		f.verify++
		return context.DeadlineExceeded
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("got %v", err)
	}
	if f.verify != 1 || f.upload != 0 || f.starts != 0 || f.job(t).Phase != "verifying" {
		t.Fatal("verification repeated or submission followed unknown verification")
	}
}

func TestDOWAYWorkflowExplicitVerificationDenialCanBeRetried(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	verify := f.d.Verify
	f.d.Verify = func(context.Context, dowayPreflight, Config, Record, cloudSession, int64, func(context.Context, string, map[string]any) (json.RawMessage, error)) error {
		f.verify++
		return &dowayRejectedError{Stage: "verification", Reason: "insufficient allowance"}
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil {
		t.Fatal("denial accepted")
	}
	if f.job(t).Phase != "verification_rejected" || f.upload != 0 {
		t.Fatal("denial was not preserved")
	}
	f.d.Verify = verify
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.verify != 2 || f.starts != 1 {
		t.Fatal("explicit denied task did not resume safely")
	}
}

func TestDOWAYWorkflowPollingCancellationResumesAccepted(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	post := f.d.Post
	f.d.Post = func(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
		if path == "/api/audio/get_result" {
			f.polls++
			return json.RawMessage(`{"content":{"orderInfo":{"status":3}}}`), nil
		}
		return post(ctx, path, body)
	}
	f.d.Wait = func(_ context.Context, delay time.Duration) error {
		if delay != 30*time.Second {
			t.Fatalf("poll interval %v", delay)
		}
		return context.Canceled
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.job(t).Phase != "accepted" || f.job(t).OrderID != "test-order" {
		t.Fatal("accepted job not preserved")
	}
	f.d.Post = post
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 || f.verify != 1 {
		t.Fatal("accepted job submitted twice")
	}
}

func TestDOWAYWorkflowUploadOutcomeResumesWithoutOverwrite(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	f.d.Upload = func(context.Context, string, string, string) error { f.upload++; return context.Canceled }
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.d.Exists = func(_ context.Context, bucket, key string) (bool, error) {
		if bucket != "useastxnote" || key != f.job(t).UploadKey {
			t.Fatal("wrong upload existence check")
		}
		return true, nil
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.upload != 1 || f.verify != 1 || f.starts != 1 {
		t.Fatal("resumed upload repeated side effects")
	}
}

func TestDOWAYWorkflowRejectsChangedInputWithoutAnotherRequest(t *testing.T) {
	for _, changed := range []string{"audio", "language", "account", "device"} {
		t.Run(changed, func(t *testing.T) {
			f := newDOWAYWorkflowFixture(t)
			if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
				t.Fatal(err)
			}
			switch changed {
			case "audio":
				f.d.Inspect = func(context.Context, string) (dowayAudioInfo, error) {
					return dowayAudioInfo{SHA256: "different", Size: 58220, Duration: 7}, nil
				}
			case "language":
				f.c.Language = "ja"
			case "device":
				f.c.Serial = "different"
			case "account":
				if err := atomicJSON(f.s.path(".work/doway-session.json"), cloudSession{PlayerID: json.Number("123"), Token: "other-session"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil {
				t.Fatal("changed task accepted")
			}
			if f.starts != 1 || f.polls != 1 || f.verify != 1 {
				t.Fatal("changed input sent requests")
			}
		})
	}
}

func TestDOWAYWorkflowOSSResultUsesMetadataBucketAndInnerParser(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	post := f.d.Post
	f.d.Post = func(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
		if path == "/api/audio/get_result" {
			f.polls++
			return json.RawMessage(`{"code":0,"readOss":1,"content":{"orderInfo":{"status":-1}}}`), nil
		}
		return post(ctx, path, body)
	}
	f.d.Exists = func(_ context.Context, bucket, key string) (bool, error) {
		if bucket != "asiaxnote" || key != f.job(t).ResultKey {
			t.Fatal("wrong result lookup")
		}
		return true, nil
	}
	f.d.Read = func(_ context.Context, bucket, key string) ([]byte, error) {
		if bucket != "asiaxnote" || key != f.job(t).ResultKey {
			t.Fatal("wrong result download")
		}
		return []byte(strings.Replace(string(dowayCompletedFixture()), `"code":"0"`, `"readOss":1`, 1)), nil
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
}

func TestDOWAYWorkflowChinaRetainsAreaAndBucket(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	prepare := f.d.Prepare
	f.d.Prepare = func(ctx context.Context, c Config, r Record, session cloudSession, post func(context.Context, string, map[string]any) (json.RawMessage, error)) (dowayPreflight, error) {
		p, err := prepare(ctx, c, r, session, post)
		p.Area = 0
		p.Bucket = "chinaxnote"
		return p, err
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.startBody["type"] != 0 || f.startBody["buckName"] != "chinaxnote" || f.pollBody["type"] != 0 || f.job(t).ResultBucket != "chinaxnote" {
		t.Fatal("China task area or bucket was changed")
	}
}

func TestDOWAYWorkflowFailedAndUnknownStatusesNeverResubmit(t *testing.T) {
	for _, status := range []string{"-1", "2"} {
		t.Run(status, func(t *testing.T) {
			f := newDOWAYWorkflowFixture(t)
			post := f.d.Post
			f.d.Post = func(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
				if path == "/api/audio/get_result" {
					f.polls++
					return json.RawMessage(`{"content":{"orderInfo":{"status":` + status + `,"failType":2}}}`), nil
				}
				return post(ctx, path, body)
			}
			for i := 0; i < 2; i++ {
				if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil {
					t.Fatal("failed or unknown order accepted")
				}
			}
			if f.starts != 1 || f.verify != 1 || f.upload != 1 {
				t.Fatal("failed order automatically resubmitted")
			}
			if status == "-1" && (f.polls != 1 || f.job(t).Phase != "failed" || f.job(t).FailType == nil || *f.job(t).FailType != 2) {
				t.Fatal("terminal failure was not persisted or was queried again")
			}
			if status == "2" && (f.polls != 2 || f.job(t).Phase != "accepted") {
				t.Fatal("unknown status was treated as a terminal failure")
			}
		})
	}
}

func TestDOWAYWorkflowLockWaitIsCancellableBeforeNetwork(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	// Hold exactly the cross-process lock the workflow must acquire.
	h := sha256.Sum256([]byte(f.r.ID))
	lock, err := os.OpenFile(f.s.path(".work/doway-"+hex.EncodeToString(h[:])+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := f.s.transcribeDOWAY(ctx, f.c, f.r, f.d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait: %v", err)
	}
	if f.verify != 0 || f.upload != 0 || f.starts != 0 || f.polls != 0 {
		t.Fatal("sent request without owning job lock")
	}
}

func TestDOWAYWorkflowUnknownCompletionReportNeverRepeated(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	post := f.d.Post
	f.d.Post = func(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
		if path == "/api/player/add_transcription_record" {
			f.reports++
			return nil, context.DeadlineExceeded
		}
		return post(ctx, path, body)
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("report outcome: %v", err)
	}
	if j := f.job(t); j.Phase != "reporting" || j.ReportState != "submitting" || !j.CleanupDone {
		t.Fatalf("report state: %+v", j)
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil || !strings.Contains(err.Error(), "completion-report outcome is unknown") {
		t.Fatalf("resume: %v", err)
	}
	if f.reports != 1 || f.starts != 1 || f.polls != 1 || f.deletes != 2 {
		t.Fatal("uncertain report repeated side effects")
	}
	var cached TranscriptResult
	if err := readJSON(filepath.Join(f.s.Dir(f.r), ".transcription", "doway-result.json"), &cached); err != nil || cached.Text != "Hello there." {
		t.Fatal("transcript was lost after report failure")
	}
}

func TestDOWAYWorkflowCleanupRetryDoesNotRepeatReport(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	remove := f.d.Delete
	f.d.Delete = func(context.Context, string, string) error { return errors.New("cleanup interrupted") }
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if j := f.job(t); j.Phase != "completed" || j.ReportState != "completed" || j.CleanupDone {
		t.Fatal("completed report marker lost")
	}
	f.d.Delete = remove
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.reports != 1 || f.starts != 1 || f.polls != 1 || f.deletes != 2 {
		t.Fatal("cleanup retry repeated transcription or reporting")
	}
}

func TestDOWAYWorkflowPublicUploadOptInRequiredBeforeVerification(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	f.c.DOWAYPublicUpload = false
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err == nil || !strings.Contains(err.Error(), "explicit permission") {
		t.Fatalf("opt-in: %v", err)
	}
	if f.verify != 0 || f.upload != 0 || f.starts != 0 {
		t.Fatal("unapproved public upload started a billable action")
	}
}

func TestDOWAYWorkflowRevokedOptInAllowsAcceptedReadResume(t *testing.T) {
	f := newDOWAYWorkflowFixture(t)
	post := f.d.Post
	f.d.Post = func(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
		if path == "/api/audio/get_result" {
			return nil, context.Canceled
		}
		return post(ctx, path, body)
	}
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.c.DOWAYPublicUpload = false
	f.d.Post = post
	if _, err := f.s.transcribeDOWAY(context.Background(), f.c, f.r, f.d); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 || f.upload != 1 || f.verify != 1 || f.reports != 1 {
		t.Fatal("resuming accepted task repeated upload or submission")
	}
}

func TestDOWAYCompletionReportCountsUTF16AndPreservesChinaModel(t *testing.T) {
	job := dowayJob{UID: "test", Filename: "sample.mp3", Language: "en", Preflight: dowayPreflight{Area: 0}}
	body, err := dowayCompletionBody(job, cloudSession{PlayerID: json.Number("123"), Token: "test"}, TranscriptResult{Text: "A😀中"})
	if err != nil {
		t.Fatal(err)
	}
	if body["words"] != 4 || body["model"] != "openai" {
		t.Fatalf("report count/model = %v/%v", body["words"], body["model"])
	}
}
