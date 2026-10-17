package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSetupMessageTranscribe(t *testing.T) {
	raw, _ := json.Marshal(setupMessage("", Smart))
	got := string(raw)
	if !strings.Contains(got, "gemini-3.5-transcribe-live") {
		t.Errorf("model missing: %s", got)
	}
	if !strings.Contains(got, `"mode":"SMART"`) {
		t.Errorf("mode missing: %s", got)
	}
	if strings.Contains(got, "translationConfig") {
		t.Errorf("translate config should be absent: %s", got)
	}
}

func TestSetupMessageVerbatimTranslate(t *testing.T) {
	raw, _ := json.Marshal(setupMessage("tr", Verbatim))
	got := string(raw)
	if !strings.Contains(got, "translate-preview") {
		t.Errorf("model missing: %s", got)
	}
	if !strings.Contains(got, `"targetLanguageCode":"tr"`) || !strings.Contains(got, "echoTargetLanguage") {
		t.Errorf("translate config missing: %s", got)
	}
}

func TestRemainder(t *testing.T) {
	cases := []struct{ interim, final, want string }{
		{"", "", ""},
		{"hello world", "hello world", ""},
		{"hello world", "hello", "world"},
		{"hello cruel world", "hello", "cruel world"},
		{"HELLO world", "hello", "world"}, // case-insensitive compare
		{"there here", "there again", ""}, // mismatched words → drop all
		{"hello", "goodbye", ""},          // no overlap → empty
	}
	for _, c := range cases {
		if got := remainder(c.interim, c.final); got != c.want {
			t.Errorf("remainder(%q, %q) = %q, want %q", c.interim, c.final, got, c.want)
		}
	}
}

func TestRemainderPunctuation(t *testing.T) {
	// plainWord strips punctuation so "hello," aligns with "hello".
	if got := remainder("hello, world", "hello"); got != "world" {
		t.Errorf("got %q, want %q", got, "world")
	}
}

func TestComposeText(t *testing.T) {
	var translated strings.Builder
	translated.WriteString(" merhaba ")
	translated.WriteString(" dünya")
	if got := composeText(nil, "", &translated, true); got != "merhaba dünya" {
		t.Errorf("translation compose = %q", got)
	}

	var empty strings.Builder
	finals := []string{"first bit", "second bit"}
	if got := composeText(finals, " pending", &empty, false); got != "first bit second bit pending" {
		t.Errorf("transcription compose = %q", got)
	}
}

func TestRedact(t *testing.T) {
	key := "AIzaSECRET"
	if got := redact("http error for "+key+" !", key); got != "http error for !" {
		t.Errorf("got %q", got)
	}
	if got := redact("a\n\n  b", key); got != "a b" {
		t.Errorf("whitespace collapse: %q", got)
	}
}

func TestFailureMessage(t *testing.T) {
	if got := failureMessage(map[string]any{
		"error": map[string]any{"message": "API key not valid " + "AIzaX"},
	}); got != "API key not valid AIzaX" {
		t.Errorf("got %q", got)
	}
	if got := failureMessage(nil); got != MsgFailed {
		t.Errorf("nil payload: got %q", got)
	}
}

func TestModeFromConfig(t *testing.T) {
	if ModeFromConfig("verbatim") != Verbatim {
		t.Error("verbatim expected")
	}
	if ModeFromConfig("smart") != Smart || ModeFromConfig("") != Smart {
		t.Error("smart expected")
	}
}
