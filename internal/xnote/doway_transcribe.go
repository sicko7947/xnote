package xnote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/hajimehoshi/go-mp3"
)

// Only a decoded, explicit business denial is safe to retry. Transport errors
// at verify_device or start_asr may follow a successful, billable server action.
type dowayRejectedError struct{ Stage, Reason string }

func (e *dowayRejectedError) Error() string { return "DOWAY " + e.Stage + ": " + e.Reason }

type dowayJob struct {
	Version      int            `json:"version"`
	UID          string         `json:"uid"`
	RecordID     string         `json:"record_id"`
	Filename     string         `json:"filename"`
	AudioSHA256  string         `json:"audio_sha256"`
	Size         int64          `json:"size"`
	Duration     int64          `json:"duration_seconds"`
	PlayerID     string         `json:"player_id"`
	Serial       string         `json:"serial"`
	Language     string         `json:"requested_language"`
	Preflight    dowayPreflight `json:"preflight"`
	UploadKey    string         `json:"upload_key"`
	ResultKey    string         `json:"result_key"`
	ResultBucket string         `json:"result_bucket"`
	Phase        string         `json:"phase"`
	OrderID      string         `json:"order_id,omitempty"`
	FailType     *int           `json:"fail_type,omitempty"`
	ReportState  string         `json:"report_state,omitempty"`
	CleanupDone  bool           `json:"cleanup_done,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type dowayAudioInfo struct {
	SHA256   string
	Size     int64
	Duration int64
}

type dowayTranscribeDeps struct {
	Post    func(context.Context, string, map[string]any) (json.RawMessage, error)
	Prepare func(context.Context, Config, Record, cloudSession, func(context.Context, string, map[string]any) (json.RawMessage, error)) (dowayPreflight, error)
	Verify  func(context.Context, dowayPreflight, Config, Record, cloudSession, int64, func(context.Context, string, map[string]any) (json.RawMessage, error)) error
	Inspect func(context.Context, string) (dowayAudioInfo, error)
	Upload  func(context.Context, string, string, string) error
	Exists  func(context.Context, string, string) (bool, error)
	Read    func(context.Context, string, string) ([]byte, error)
	Delete  func(context.Context, string, string) error
	Wait    func(context.Context, time.Duration) error
	Now     func() time.Time
	NewUID  func() (string, error)
}

func (s *Store) TranscribeDOWAY(ctx context.Context, c Config, r Record) (TranscriptResult, error) {
	var cached dowayOSSCredentials
	oss := func(ctx context.Context) (dowayOSSClient, error) {
		if cached.AccessKeyID == "" || !cached.Expiration.After(time.Now().Add(time.Minute)) {
			credentials, err := fetchDOWAYOSSCredentials(ctx, nil)
			if err != nil {
				return dowayOSSClient{}, err
			}
			cached = credentials
		}
		return dowayOSSClient{Credentials: cached}, nil
	}
	d := dowayTranscribeDeps{
		Post: cloudPost, Prepare: prepareDOWAYTranscription, Verify: verifyDOWAYTranscription,
		Inspect: inspectDOWAYAudio, Wait: waitDOWAY, Now: time.Now,
		NewUID: func() (string, error) {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return "", err
			}
			return "xnote_" + hex.EncodeToString(id[:]), nil
		},
		Upload: func(ctx context.Context, bucket, key, path string) error {
			client, err := oss(ctx)
			if err != nil {
				return err
			}
			return client.UploadFileWithACL(ctx, bucket, key, path, c.DOWAYPublicUpload)
		},
		Exists: func(ctx context.Context, bucket, key string) (bool, error) {
			client, err := oss(ctx)
			if err != nil {
				return false, err
			}
			return client.Exists(ctx, bucket, key)
		},
		Read: func(ctx context.Context, bucket, key string) ([]byte, error) {
			client, err := oss(ctx)
			if err != nil {
				return nil, err
			}
			return client.Download(ctx, bucket, key, 64<<20)
		},
		Delete: func(ctx context.Context, bucket, key string) error {
			client, err := oss(ctx)
			if err != nil {
				return err
			}
			return client.Delete(ctx, bucket, key)
		},
	}
	return s.transcribeDOWAY(ctx, c, r, d)
}

func inspectDOWAYAudio(ctx context.Context, path string) (dowayAudioInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return dowayAudioInfo{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return dowayAudioInfo{}, err
	}
	if !stat.Mode().IsRegular() || stat.Size() == 0 {
		return dowayAudioInfo{}, errors.New("DOWAY requires a nonempty MP3 file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, contextAudioFile{ctx: ctx, File: f}); err != nil {
		return dowayAudioInfo{}, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return dowayAudioInfo{}, err
	}
	decoder, err := mp3.NewDecoder(contextAudioFile{ctx: ctx, File: f})
	if err != nil {
		return dowayAudioInfo{}, fmt.Errorf("DOWAY MP3 duration: %w", err)
	}
	if decoder.SampleRate() <= 0 || decoder.Length() <= 0 {
		return dowayAudioInfo{}, errors.New("DOWAY MP3 duration unavailable")
	}
	// The app sends whole seconds (its import path divides milliseconds by 1000).
	duration := decoder.Length() / int64(decoder.SampleRate()*4)
	if duration < 1 {
		return dowayAudioInfo{}, errors.New("DOWAY requires at least one second of audio")
	}
	return dowayAudioInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: stat.Size(), Duration: duration}, ctx.Err()
}

func waitDOWAY(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Store) transcribeDOWAY(ctx context.Context, c Config, r Record, d dowayTranscribeDeps) (TranscriptResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	if !safePart(r.ID) || r.Trashed || r.Audio == "" {
		return TranscriptResult{}, errors.New("DOWAY recording is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return TranscriptResult{}, err
	}
	// The lock survives clearing the recording's transcript directory. No second
	// process can race the durable before-request markers and submit twice.
	lockHash := sha256.Sum256([]byte(r.ID))
	lock, err := os.OpenFile(s.path(".work/doway-"+hex.EncodeToString(lockHash[:])+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return TranscriptResult{}, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return TranscriptResult{}, err
		}
		if err = waitDOWAY(ctx, 100*time.Millisecond); err != nil {
			return TranscriptResult{}, err
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	session, err := s.cloudSession()
	if err != nil {
		return TranscriptResult{}, err
	}
	audio, err := d.Inspect(ctx, r.Audio)
	if err != nil {
		return TranscriptResult{}, err
	}
	dir := filepath.Join(s.Dir(r), ".transcription")
	jobPath := filepath.Join(dir, "doway-job.json")
	resultPath := filepath.Join(dir, "doway-result.json")
	var job dowayJob
	err = readJSON(jobPath, &job)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return TranscriptResult{}, fmt.Errorf("DOWAY saved job: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		if !c.DOWAYPublicUpload {
			return TranscriptResult{}, errors.New("DOWAY requires explicit permission for public audio upload in provider settings")
		}
		if strings.TrimSpace(r.DeviceName) == "" {
			return TranscriptResult{}, errors.New("DOWAY recording filename is required for completion reporting")
		}
		p, err := d.Prepare(ctx, c, r, session, d.Post)
		if err != nil {
			return TranscriptResult{}, err
		}
		// The shared HTTPS controller accepts area 0 as well as area 1. Keep
		// the device's real area and bucket; never use the app's cleartext route.
		if p.Area != 0 && p.Area != 1 {
			return TranscriptResult{}, errors.New("DOWAY returned an unsupported transcription area")
		}
		uid, err := d.NewUID()
		if err != nil {
			return TranscriptResult{}, err
		}
		uploadKey, err := dowayAudioObjectKey(c.Serial, session.PlayerID.String(), uid)
		if err != nil {
			return TranscriptResult{}, err
		}
		resultKey, err := dowayTranscriptObjectKey(session.PlayerID.String(), uid)
		if err != nil {
			return TranscriptResult{}, err
		}
		job = dowayJob{Version: 1, UID: uid, RecordID: r.ID, Filename: r.DeviceName, AudioSHA256: audio.SHA256, Size: audio.Size,
			Duration: audio.Duration, PlayerID: session.PlayerID.String(), Serial: c.Serial, Language: c.Language,
			Preflight: p, UploadKey: uploadKey, ResultKey: resultKey, ResultBucket: "asiaxnote", Phase: "prepared"}
		if p.Area == 0 {
			job.ResultBucket = "chinaxnote"
		}
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
	} else if job.Version != 1 || job.RecordID != r.ID || job.AudioSHA256 != audio.SHA256 || job.Size != audio.Size ||
		job.PlayerID != session.PlayerID.String() || job.Serial != c.Serial || job.Language != c.Language {
		return TranscriptResult{}, errors.New("DOWAY saved job belongs to different audio, account, device or language; refusing another submission")
	}
	if err = validateDOWAYJob(job, c, session); err != nil {
		return TranscriptResult{}, err
	}
	if job.Filename == "" {
		job.Filename = r.DeviceName
	}
	if job.Phase == "failed" {
		if err = cleanupDOWAYJob(ctx, jobPath, &job, d); err != nil {
			return TranscriptResult{}, err
		}
		return TranscriptResult{}, &dowayTaskFailure{FailType: job.FailType}
	}
	recordFailure := func(err error) error {
		var failed *dowayTaskFailure
		if errors.As(err, &failed) {
			job.Phase, job.FailType = "failed", failed.FailType
			if saveErr := saveDOWAYJob(jobPath, &job, d.Now); saveErr != nil {
				return saveErr
			}
			if cleanupErr := cleanupDOWAYJob(ctx, jobPath, &job, d); cleanupErr != nil {
				return cleanupErr
			}
		}
		return err
	}
	if job.Phase == "completed" {
		var result TranscriptResult
		if err = readJSON(resultPath, &result); err != nil || strings.TrimSpace(result.Text) == "" {
			return TranscriptResult{}, errors.New("DOWAY saved transcript is missing or invalid")
		}
		if job.ReportState != "completed" {
			return TranscriptResult{}, errors.New("DOWAY transcript is cached but its completion-report status is unknown; check the DOWAY app before further submissions")
		}
		if err = cleanupDOWAYJob(ctx, jobPath, &job, d); err != nil {
			return TranscriptResult{}, err
		}
		return result, nil
	}
	if job.Phase == "result_ready" || job.Phase == "reporting" {
		return finishDOWAYTranscription(ctx, session, jobPath, resultPath, &job, d)
	}
	if !c.DOWAYPublicUpload {
		switch job.Phase {
		case "prepared", "verification_rejected", "verified", "uploading", "uploaded":
			return TranscriptResult{}, errors.New("DOWAY public audio upload permission is disabled; the saved job was not submitted")
		}
	}
	if job.Phase == "verifying" {
		return TranscriptResult{}, errors.New("DOWAY verification outcome is unknown; check the DOWAY app before retrying (no request was repeated)")
	}
	if job.Phase == "prepared" || job.Phase == "verification_rejected" {
		job.Phase = "verifying"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
		if err = d.Verify(ctx, job.Preflight, c, r, session, job.Duration, d.Post); err != nil {
			var rejected *dowayRejectedError
			if errors.As(err, &rejected) {
				job.Phase = "verification_rejected"
				if saveErr := saveDOWAYJob(jobPath, &job, d.Now); saveErr != nil {
					return TranscriptResult{}, saveErr
				}
			}
			return TranscriptResult{}, err
		}
		job.Phase = "verified"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
	}
	if job.Phase == "verified" || job.Phase == "uploading" {
		exists := false
		if job.Phase == "uploading" {
			exists, err = d.Exists(ctx, job.Preflight.Bucket, job.UploadKey)
			if err != nil {
				return TranscriptResult{}, err
			}
		}
		job.Phase = "uploading"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
		if !exists {
			if err = d.Upload(ctx, job.Preflight.Bucket, job.UploadKey, r.Audio); err != nil {
				return TranscriptResult{}, err
			}
		}
		job.Phase = "uploaded"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
	}
	if job.Phase == "uploaded" {
		check, err := d.Inspect(ctx, r.Audio)
		if err != nil {
			return TranscriptResult{}, err
		}
		if check.SHA256 != job.AudioSHA256 || check.Size != job.Size {
			return TranscriptResult{}, errors.New("DOWAY audio changed during upload; submission stopped")
		}
		if err = ctx.Err(); err != nil {
			return TranscriptResult{}, err
		}
		job.Phase = "submitting"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
		raw, err := d.Post(ctx, "/api/audio/start_asr", dowayStartBody(job, session))
		if err != nil {
			return TranscriptResult{}, fmt.Errorf("DOWAY submission outcome may be unknown; retry will query the same job: %w", err)
		}
		_, orderID, err := parseDOWAYStart(raw)
		if err != nil {
			return TranscriptResult{}, err
		}
		job.OrderID, job.Phase = orderID, "accepted"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
	}
	if job.Phase != "accepted" && job.Phase != "submitting" {
		return TranscriptResult{}, fmt.Errorf("DOWAY saved job has unsupported phase %q", job.Phase)
	}
	for {
		if err = ctx.Err(); err != nil {
			return TranscriptResult{}, err
		}
		raw, err := d.Post(ctx, "/api/audio/get_result", dowayPollBody(job, session, 2))
		if err != nil {
			if job.Phase == "submitting" {
				return TranscriptResult{}, fmt.Errorf("DOWAY submission outcome is unknown; original job could not be queried and was not resubmitted: %w", err)
			}
			return TranscriptResult{}, err
		}
		status, readOSS, orderResult, err := parseDOWAYPoll(raw)
		if err != nil {
			err = recordFailure(err)
			if job.Phase == "submitting" {
				return TranscriptResult{}, fmt.Errorf("DOWAY submission outcome is unknown; check the DOWAY app (no request was repeated): %w", err)
			}
			return TranscriptResult{}, err
		}
		if readOSS {
			exists, err := d.Exists(ctx, job.ResultBucket, job.ResultKey)
			if err != nil {
				return TranscriptResult{}, err
			}
			if exists {
				body, err := d.Read(ctx, job.ResultBucket, job.ResultKey)
				if err != nil {
					return TranscriptResult{}, err
				}
				status, orderResult, err = parseDOWAYStoredResult(body)
				if err != nil {
					return TranscriptResult{}, recordFailure(err)
				}
			} else {
				// App 3.7.7 requests version 1 once when its version 2 result is
				// not in OSS. This is a read-only query, never a second start.
				raw, err = d.Post(ctx, "/api/audio/get_result", dowayPollBody(job, session, 1))
				if err != nil {
					return TranscriptResult{}, err
				}
				status, readOSS, orderResult, err = parseDOWAYPoll(raw)
				if err != nil {
					return TranscriptResult{}, recordFailure(err)
				}
				if readOSS {
					return TranscriptResult{}, errors.New("DOWAY result file is not available yet; retry to resume this job")
				}
			}
		}
		if status == 4 {
			result, err := parseDOWAYOrderResult([]byte(orderResult))
			if err != nil {
				return TranscriptResult{}, err
			}
			if err = atomicJSON(resultPath, result); err != nil {
				return TranscriptResult{}, err
			}
			job.Phase, job.ReportState = "result_ready", "pending"
			if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
				return TranscriptResult{}, err
			}
			return finishDOWAYTranscription(ctx, session, jobPath, resultPath, &job, d)
		}
		job.Phase = "accepted"
		if err = saveDOWAYJob(jobPath, &job, d.Now); err != nil {
			return TranscriptResult{}, err
		}
		if err = d.Wait(ctx, 30*time.Second); err != nil {
			return TranscriptResult{}, err
		}
	}
}

func finishDOWAYTranscription(ctx context.Context, session cloudSession, jobPath, resultPath string, job *dowayJob, d dowayTranscribeDeps) (TranscriptResult, error) {
	if job.PlayerID != session.PlayerID.String() {
		return TranscriptResult{}, errors.New("DOWAY completion report account does not match the saved job")
	}
	if err := validateDOWAYJob(*job, Config{Serial: job.Serial}, session); err != nil {
		return TranscriptResult{}, err
	}
	var result TranscriptResult
	if err := readJSON(resultPath, &result); err != nil || strings.TrimSpace(result.Text) == "" {
		return TranscriptResult{}, errors.New("DOWAY completion report requires the cached transcript")
	}
	if job.Phase == "reporting" {
		if err := cleanupDOWAYJob(ctx, jobPath, job, d); err != nil {
			return TranscriptResult{}, err
		}
		return TranscriptResult{}, errors.New("DOWAY transcript is cached; completion-report outcome is unknown, so it was not repeated; check the DOWAY app")
	}
	if job.Phase != "result_ready" || job.ReportState != "pending" {
		return TranscriptResult{}, errors.New("DOWAY completion report has no pending saved result")
	}
	body, err := dowayCompletionBody(*job, session, result)
	if err != nil {
		return TranscriptResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return TranscriptResult{}, err
	}
	job.Phase, job.ReportState = "reporting", "submitting"
	if err = saveDOWAYJob(jobPath, job, d.Now); err != nil {
		return TranscriptResult{}, err
	}
	if _, err = d.Post(ctx, "/api/player/add_transcription_record", body); err != nil {
		// The App can refresh account data even on a non-200 business code.
		// Treat all unacknowledged reports as uncertain, never safely replayable.
		cleanupErr := cleanupDOWAYJob(ctx, jobPath, job, d)
		return TranscriptResult{}, errors.Join(fmt.Errorf("DOWAY transcript is cached; completion-report outcome is unknown and will not be repeated: %w", err), cleanupErr)
	}
	job.Phase, job.ReportState = "completed", "completed"
	if err = saveDOWAYJob(jobPath, job, d.Now); err != nil {
		return TranscriptResult{}, err
	}
	if err = cleanupDOWAYJob(ctx, jobPath, job, d); err != nil {
		return TranscriptResult{}, err
	}
	return result, nil
}

func dowayCompletionBody(j dowayJob, session cloudSession, result TranscriptResult) (map[string]any, error) {
	language, err := dowayLanguagePreflight(j.Language)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(j.Filename) == "" || strings.TrimSpace(result.Text) == "" || language.ReportLanguage == "" || language.ReportLanguageCode == "" {
		return nil, errors.New("DOWAY completion report is missing its filename, transcript, or language")
	}
	model := "openai"
	if j.Preflight.Area == 1 {
		model = "aliyun"
	}
	return map[string]any{"fileUid": j.UID, "playerId": session.PlayerID, "filename": j.Filename,
		"duration": j.Duration, "token": session.Token, "sn": j.Serial, "lang": language.ReportLanguage,
		"words": len(utf16.Encode([]rune(result.Text))), "type": 1, "model": model, "engineType": 0,
		"categoryTYpe": "all", "summaryType": "custom", "templateId": 0, "langID": language.LanguageID,
		"langCode": language.ReportLanguageCode, "unlimited": j.Preflight.Unlimited, "costTime": 0}, nil
}

func cleanupDOWAYJob(ctx context.Context, jobPath string, job *dowayJob, d dowayTranscribeDeps) error {
	if job.CleanupDone {
		return nil
	}
	if d.Delete == nil {
		return errors.New("DOWAY task is retained because object cleanup is unavailable")
	}
	// Both keys are derived from this job and validated before reaching here.
	// The cached transcript is durable before a successful task is cleaned up.
	for _, object := range [][2]string{{job.Preflight.Bucket, job.UploadKey}, {job.ResultBucket, job.ResultKey}} {
		if err := d.Delete(ctx, object[0], object[1]); err != nil {
			return fmt.Errorf("DOWAY task retained for object cleanup retry: %w", err)
		}
	}
	job.CleanupDone = true
	return saveDOWAYJob(jobPath, job, d.Now)
}

func validateDOWAYJob(j dowayJob, c Config, session cloudSession) error {
	upload, err := dowayAudioObjectKey(c.Serial, session.PlayerID.String(), j.UID)
	if err != nil {
		return errors.New("DOWAY saved job has invalid identity")
	}
	result, err := dowayTranscriptObjectKey(session.PlayerID.String(), j.UID)
	if err != nil {
		return errors.New("DOWAY saved job has invalid identity")
	}
	resultBucket := "asiaxnote"
	uploadBucket := dowayTranscriptionBucket(j.Preflight.Region)
	if j.Preflight.Area == 0 {
		resultBucket, uploadBucket = "chinaxnote", "chinaxnote"
	}
	if (j.Preflight.Area != 0 && j.Preflight.Area != 1) || j.UploadKey != upload || j.ResultKey != result || j.ResultBucket != resultBucket || j.Preflight.Bucket != uploadBucket || j.Duration <= 0 {
		return errors.New("DOWAY saved job has inconsistent object paths or transcription settings")
	}
	return nil
}

func saveDOWAYJob(path string, job *dowayJob, now func() time.Time) error {
	job.UpdatedAt = now().UTC()
	if err := atomicJSON(path, job); err != nil {
		return err
	}
	// The directory sync makes the atomic rename durable before a billable
	// request can be sent, including a crash immediately after POST.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func dowayStartBody(j dowayJob, s cloudSession) map[string]any {
	return map[string]any{"fileName": j.UploadKey, "playerId": s.PlayerID, "fileSize": j.Size,
		"duration": j.Duration, "language": j.Preflight.Language, "fileUid": j.UID, "type": j.Preflight.Area,
		"token": s.Token, "roleType": j.Preflight.RoleType, "engRLang": j.Preflight.EngRLang,
		"fileKey": j.ResultKey, "version": 2, "buckName": j.Preflight.Bucket}
}

func dowayPollBody(j dowayJob, s cloudSession, version int) map[string]any {
	return map[string]any{"playerId": s.PlayerID, "orderId": j.OrderID, "fileUid": j.UID, "type": j.Preflight.Area,
		"token": s.Token, "fileName": j.UploadKey, "version": version, "bucket": j.ResultBucket,
		"region": j.Preflight.PollRegion, "fileKey": j.ResultKey}
}
