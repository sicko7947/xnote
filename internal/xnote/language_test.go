package xnote

import "testing"

func TestDetectTranscriptLanguage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript string
		want       string
	}{
		{"chinese", "我们今天讨论一下产品集成和后续的数据策略。", "zh-CN"},
		{"english", "So, hmm, where do we get started? Product overview.", "en"},
		{"japanese", "はい、そうですね。今日はよろしくお願いします。", "ja"},
		{"japanese-kanji-heavy", "株式会社の件につきまして、明日までにご確認をお願いいたします。", "ja"},
		{"chinese-with-katakana-loanword", "这个 トヨタ 的案子我们要再看一下，预算和进度都要重新排。", "zh-CN"},
		{"chinese-with-latin-terms", "这个 Signal 的数据策略我们先对一下，UI 的部分下周再排期。", "zh-CN"},
		{"english-with-cjk-quote", `He said "你好" and moved on to the next topic.`, "en"},
		{"empty", "", ""},
		{"punctuation-and-digits-only", "12345 ... --- ??? ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectTranscriptLanguage(tc.transcript); got != tc.want {
				t.Fatalf("DetectTranscriptLanguage(%q) = %q, want %q", tc.transcript, got, tc.want)
			}
		})
	}
}

// "auto" must follow the recording, and only fall back to the interface
// language when the transcript says nothing about its language.
func TestSummaryLanguageForFollowsRecording(t *testing.T) {
	chinese := Record{Transcript: "我们今天讨论一下产品集成。"}
	english := Record{Transcript: "Let us review the integration plan."}
	japanese := Record{Transcript: "はい、では始めましょう。"}
	blank := Record{Transcript: "12345 ---"}

	for _, tc := range []struct {
		name string
		c    Config
		r    Record
		want string
	}{
		{"auto-chinese", Config{SummaryLanguage: "auto", Locale: "en"}, chinese, "zh"},
		{"auto-english", Config{SummaryLanguage: "auto", Locale: "zh-CN"}, english, "en"},
		{"auto-japanese", Config{SummaryLanguage: "auto", Locale: "zh-CN"}, japanese, "ja"},
		{"auto-unclassifiable-falls-back-to-locale", Config{SummaryLanguage: "auto", Locale: "en"}, blank, "en"},
		{"explicit-wins-over-recording", Config{SummaryLanguage: "ja", Locale: "en"}, english, "ja"},
		{"legacy-empty-follows-locale", Config{SummaryLanguage: "", Locale: "zh-CN"}, english, "zh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := summaryLanguageFor(tc.c, tc.r)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("summaryLanguageFor() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A stored done summary must become stale when the language setting changes,
// otherwise there is no way to re-run it in the new language.
func TestSummaryOptionsKeyTracksLanguage(t *testing.T) {
	base := summaryOptionsKey(Config{})
	if base == "none" {
		t.Fatal("summary-only request reported as disabled")
	}
	for _, language := range []string{"auto", "zh-CN", "en", "ja"} {
		if got := summaryOptionsKey(Config{SummaryLanguage: language}); got == base {
			t.Fatalf("summary language %q did not change the request key", language)
		}
	}
	// The disabled sentinel stays bare so callers can test for it directly.
	if got := summaryOptionsKey(Config{SummaryDisabled: true}); got != "none" {
		t.Fatalf("disabled outputs = %q, want none", got)
	}
	// A language suffix must not change which outputs are restored.
	c := summaryConfigForOptions(Config{}, "summary-mindmap|auto")
	if !c.MindmapEnabled || c.SummaryDisabled {
		t.Fatalf("language suffix broke option restore: %+v", c)
	}
	// Legacy values written before the language existed stay resumable.
	if legacySummaryOutputs("") != summaryOptionsKey(Config{})[:len("summary")] {
		t.Fatal("legacy summary-only job no longer matches")
	}
}
