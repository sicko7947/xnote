package xnote

import "unicode"

// summaryLanguageAuto tells the summary engine to follow each recording's own
// language instead of one fixed choice for the whole library.
const summaryLanguageAuto = "auto"

// Detection is script-based rather than driven by a language field on the
// transcription response: it is provider-agnostic (DOWAY, ElevenLabs, a
// bring-your-own API and offline whisper all feed the same transcript), costs
// no extra request, is deterministic in tests, and applies retroactively to
// recordings transcribed before this setting existed.
//
// The backend accepts only Chinese, English and Japanese, so telling Han, Kana
// and Latin apart is exactly enough. Latin-script languages other than English
// cannot be distinguished this way; they resolve to English, the closest
// language the backend offers.
//
// minJapaneseKanaShare is the kana share of a transcript's Chinese-character
// scripts (Han + Kana) above which it counts as Japanese. Japanese always mixes
// kana into its kanji — natural speech transcripts sit far above half — while
// Chinese never uses kana at all, so this only has to reject the incidental
// katakana loanword that Chinese text occasionally carries.
const minJapaneseKanaShare = 0.20

// DetectTranscriptLanguage reports which summary language a transcript is
// written in: "zh-CN", "en" or "ja". It returns "" when the text holds no
// classifiable letters (empty, numbers and punctuation only), which callers
// treat as "no opinion".
func DetectTranscriptLanguage(transcript string) string {
	han, kana, latin := 0, 0, 0
	for _, r := range transcript {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			kana++
		case unicode.In(r, unicode.Han):
			han++
		case unicode.In(r, unicode.Latin):
			latin++
		}
	}
	cjk := han + kana
	switch {
	case cjk+latin == 0:
		return ""
	// Compare the two script families first, so an English sentence quoting a
	// Japanese product name stays English.
	case latin > cjk:
		return "en"
	case float64(kana) >= minJapaneseKanaShare*float64(cjk):
		return "ja"
	default:
		return "zh-CN"
	}
}

// summaryLanguageFor resolves the summary language for one recording. An
// explicit choice wins; "auto" follows the recording itself; and the legacy
// empty value keeps following the interface language. "auto" falls back to the
// interface language only when the transcript carries nothing classifiable.
func summaryLanguageFor(c Config, r Record) (string, error) {
	if c.SummaryLanguage == summaryLanguageAuto {
		if detected := DetectTranscriptLanguage(r.Transcript); detected != "" {
			return summaryLanguageCode(detected)
		}
		return dowaySummaryLanguage(Config{Locale: c.Locale})
	}
	return dowaySummaryLanguage(c)
}
