package xnote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This is safe to persist with a pending task: it contains no credentials.
type dowayPreflight struct {
	Language           string
	LanguageID         string
	VerifyLanguageCode string
	ReportLanguage     string
	ReportLanguageCode string
	EngRLang           int
	RoleType           int
	Area               int
	Region             string
	PollRegion         string
	Bucket             string
	Unlimited          int
}

// The bundled app's LanguageCfg and XFAsrHelper.getFileAsrEngine select xfyun
// (file engine 0) for these models because xfyunOverseasRaAsrCode is nonempty.
// The start_asr "type" field is the area, not this internal engine enum.
func dowayLanguagePreflight(language string) (dowayPreflight, error) {
	p := dowayPreflight{VerifyLanguageCode: "en", Unlimited: 1}
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "zh", "zh-cn", "zh-hans", "cn":
		p.Language, p.LanguageID = "cn", "1"
		p.ReportLanguage, p.ReportLanguageCode = "zh-Hans", "zh_cn"
	case "en", "en-us":
		p.Language, p.LanguageID = "en", "3"
		p.ReportLanguage, p.ReportLanguageCode = "en", "en_us"
	case "ja", "ja-jp":
		p.Language, p.LanguageID = "ja", "4"
		p.ReportLanguage, p.ReportLanguageCode = "ja", "ja_jp"
	case "ko", "ko-kr":
		p.Language, p.LanguageID = "ko", "5"
		p.ReportLanguage, p.ReportLanguageCode = "ko", "ko_kr"
	case "fr", "fr-fr":
		p.Language, p.LanguageID = "fr", "6"
		p.ReportLanguage, p.ReportLanguageCode = "fr", "fr_fr"
	case "es", "es-es":
		p.Language, p.LanguageID = "es", "7"
		p.ReportLanguage, p.ReportLanguageCode = "es", "es_es"
	case "ru", "ru-ru":
		p.Language, p.LanguageID = "ru", "9"
		p.ReportLanguage, p.ReportLanguageCode = "ru", "ru_ru"
	case "de", "de-de":
		p.Language, p.LanguageID = "de", "11"
		p.ReportLanguage, p.ReportLanguageCode = "de", "de_DE"
	case "it", "it-it":
		p.Language, p.LanguageID = "it", "12"
		p.ReportLanguage, p.ReportLanguageCode = "it", "it_IT"
	case "vi", "vi-vn":
		p.Language, p.LanguageID = "vi", "22"
		p.ReportLanguage, p.ReportLanguageCode = "vi", "vi_VN"
	case "ar":
		p.Language, p.LanguageID = "ar", "31"
		p.ReportLanguage, p.ReportLanguageCode = "ar", "ar_il"
	case "", "auto":
		return dowayPreflight{}, errors.New("DOWAY all-language automatic detection has no verified request contract; no recording was submitted")
	default:
		return dowayPreflight{}, errors.New("DOWAY language is not yet supported by this client; supported languages: zh, en, ja, ko, fr, es, ru, de, it, vi, ar")
	}
	// transcription_helper.dart passes the recording's previous asrLanguageCode
	// (or "en" when absent) to verify, then sets the selected code on success.
	// Local recordings have no previous DOWAY language, so verify uses "en".
	// Completion reports instead use msTranslatorCode and getFileAsrCode(),
	// which selects the nonempty RA code for every supported model above.
	return p, nil
}

func dowayPreflightIdentity(c Config, r Record, session cloudSession) error {
	playerID, err := session.PlayerID.Int64()
	if err != nil || playerID <= 0 || strings.TrimSpace(session.Token) == "" {
		return errors.New("DOWAY login required")
	}
	if r.Serial == "" || r.Serial != c.Serial {
		return errors.New("DOWAY recording does not match the configured device")
	}
	return nil
}

// prepareDOWAYTranscription only reads authenticated device/account metadata.
// It does not verify quota, upload audio, or create a transcription task.
func prepareDOWAYTranscription(ctx context.Context, c Config, r Record, session cloudSession, post func(context.Context, string, map[string]any) (json.RawMessage, error)) (dowayPreflight, error) {
	if err := ctx.Err(); err != nil {
		return dowayPreflight{}, err
	}
	p, err := dowayLanguagePreflight(c.Language)
	if err != nil {
		return dowayPreflight{}, err
	}
	if err := dowayPreflightIdentity(c, r, session); err != nil {
		return dowayPreflight{}, err
	}
	data, err := post(ctx, "/api/device/get_device_info", map[string]any{"sn": r.Serial, "token": session.Token})
	if err != nil {
		return dowayPreflight{}, fmt.Errorf("DOWAY device information: %w", err)
	}
	var device struct {
		Serial     string      `json:"sn"`
		PlayerID   json.Number `json:"playerId"`
		Area       *int        `json:"areaType"`
		ChargePlan *int        `json:"chargePlan"`
		UseAWS     *int        `json:"useAwsS1"`
	}
	if err := json.Unmarshal(data, &device); err != nil {
		return dowayPreflight{}, errors.New("DOWAY returned invalid device information")
	}
	if device.Serial != r.Serial || device.PlayerID != session.PlayerID {
		return dowayPreflight{}, errors.New("DOWAY device information does not match the recording and signed-in account")
	}
	// AreaUtils.getAreaType compares DeviceInfo.areaType with the enum index:
	// china=0, overseas=1; a missing/unrecognized value defaults to overseas.
	p.Area = 1
	if device.Area != nil && *device.Area == 0 {
		p.Area = 0
	}
	// RecordHttpHelper.verifyDevice forces this flag for chargePlan 2. This
	// flag is a protocol input, not proof of an unlimited/free entitlement.
	if device.ChargePlan != nil && *device.ChargePlan == 2 {
		p.Unlimited = 1
	}
	if err := ctx.Err(); err != nil {
		return dowayPreflight{}, err
	}
	// China uses a fixed bucket and an empty poll region, so account-region
	// metadata is unnecessary after the device owner has already been checked.
	if p.Area == 0 {
		p.Bucket = "chinaxnote"
		return p, nil
	}
	data, err = post(ctx, "/api/player/get", map[string]any{"playerId": session.PlayerID, "token": session.Token})
	if err != nil {
		return dowayPreflight{}, fmt.Errorf("DOWAY account information: %w", err)
	}
	var account struct {
		PlayerID json.Number `json:"playerId"`
		Region   string      `json:"awsRegion"`
	}
	if err := json.Unmarshal(data, &account); err != nil {
		return dowayPreflight{}, errors.New("DOWAY returned invalid account information")
	}
	if account.PlayerID != session.PlayerID {
		return dowayPreflight{}, errors.New("DOWAY account information does not match the signed-in account")
	}
	// AliyunOssTranscriptionMgr.getRegion reads DoWayUser.awsRegion. Its
	// getBuckName returns chinaxnote for area 0, otherwise selects by region.
	p.Region = account.Region
	if p.Region == "" {
		p.Region = "ap-southeast-1"
	}
	p.Bucket = dowayTranscriptionBucket(p.Region)
	// StorageMgr.getRegion only returns the account region when its AWS
	// transcription backend is enabled for an overseas device.
	if device.UseAWS != nil && *device.UseAWS != 0 && p.Area == 1 {
		p.PollRegion = p.Region
	}
	return p, nil
}

// verifyDOWAYTranscription must only be called after the workflow durably marks
// the task as verifying. The app does not establish that this request is free
// of quota side effects. Send exactly once and propagate an ambiguous error.
func verifyDOWAYTranscription(ctx context.Context, p dowayPreflight, c Config, r Record, session cloudSession, duration int64, post func(context.Context, string, map[string]any) (json.RawMessage, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := dowayPreflightIdentity(c, r, session); err != nil {
		return err
	}
	if duration <= 0 {
		return errors.New("DOWAY requires a positive recording duration in seconds")
	}
	language, err := dowayLanguagePreflight(p.Language)
	if err != nil || p.LanguageID != language.LanguageID || p.VerifyLanguageCode != "en" || p.Unlimited != 1 || p.RoleType != 0 || p.EngRLang != 0 {
		return errors.New("DOWAY transcription preflight is invalid")
	}
	_, err = post(ctx, "/api/device/verify_device", map[string]any{
		"sn": r.Serial, "playerId": session.PlayerID, "token": session.Token,
		"time": duration, "langID": p.LanguageID, "langCode": p.VerifyLanguageCode,
		"unlimited": p.Unlimited,
	})
	// cloudPost only returns nil error for HTTP 200 + integer envelope code 200.
	// The app accepts this envelope regardless of the data value. Do not guess
	// that null/false data means rejection or repeat a successful verification.
	return err
}
