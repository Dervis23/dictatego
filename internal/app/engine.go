// Package app hosts the dictation engine: the state machine the daemon
// (and one-shot mode) drive over IPC or CLI.
package app

import (
	"context"
	"os/exec"
	"sync"
	"time"

	"dictatego/internal/audio"
	"dictatego/internal/config"
	"dictatego/internal/dictionary"
	"dictatego/internal/gemini"
)

// State of the engine.
type State string

const (
	StateIdle      State = "idle"
	StateRecording State = "recording"
	StateFinishing State = "finishing"
)

// PasteFunc delivers final text to the active window (wired in Faz 3).
// Returning an error means only the clipboard fallback remains.
type PasteFunc func(text string) error

// Engine owns recorder/session cross-cutting state.
type Engine struct {
	cfg *config.Config
	// reloadMu guards cfg swaps done by `set` commands.
	reloadMu sync.Mutex

	rec  *audio.Recorder
	sess *gemini.Session

	mu      sync.Mutex
	state   State
	preview string

	// flowMu serializes the start/stop flows: a double keybind press plus a
	// tray click must not open two mic/session pairs (or two stops).
	flowMu sync.Mutex

	paste  PasteFunc
	notify func(summary, body string)
	hook   func(State)
}

// SetStateHook receives every engine state transition (tray icon sync).
func (e *Engine) SetStateHook(f func(State)) {
	e.mu.Lock()
	e.hook = f
	st := e.state
	e.mu.Unlock()
	if f != nil {
		f(st)
	}
}

// New builds the engine with notify-send as reporter and the tool-injection
// paste chain.
func New(cfg *config.Config) *Engine {
	e := &Engine{
		cfg:    cfg,
		state:  StateIdle,
		notify: notifySend,
	}
	e.rec = audio.NewRecorder(e.onChunk)
	e.paste = pasteWithTools
	return e
}

// SetPasteFunc swaps the injector (used by Faz 3 wiring and tests).
func (e *Engine) SetPasteFunc(f PasteFunc) {
	e.mu.Lock()
	e.paste = f
	e.mu.Unlock()
}

// SetNotifier swaps the reporter (tests inject a capture).
func (e *Engine) SetNotifier(f func(summary, body string)) { e.notify = f }

// Config returns the live config snapshot (ReloadConfig swaps it under lock).
func (e *Engine) Config() *config.Config {
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()
	return e.cfg
}

// ReloadConfig re-reads settings from disk (a `set` may have changed them).
func (e *Engine) ReloadConfig() error {
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	e.cfg = cfg
	return nil
}

func (e *Engine) onChunk(pcm []byte) {
	e.mu.Lock()
	sess := e.sess
	e.mu.Unlock()
	if sess != nil {
		sess.Push(pcm)
	}
}

// State reports the current state plus the live transcript preview.
func (e *Engine) State() (State, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	preview := e.preview
	if e.state == StateRecording && e.sess != nil {
		preview = e.sess.Preview()
	}
	return e.state, preview
}

// Toggle starts recording, or — when already recording — stops and
// finalizes exactly like a press-to-toggle hotkey.
func (e *Engine) Toggle(ctx context.Context) (State, string, error) {
	// Sıraya gir: çift bind basışı + tray tıklaması aynı anda iki akış
	// başlatamaz (iki mikrofon/oturum açma veya çift stop).
	e.flowMu.Lock()
	defer e.flowMu.Unlock()

	e.mu.Lock()
	st := e.state
	e.mu.Unlock()
	switch st {
	case StateRecording:
		text, err := e.stopAndFinalize(ctx)
		return StateIdle, text, err
	case StateFinishing:
		return StateFinishing, "", nil
	default:
		if err := e.start(ctx); err != nil {
			// CLI çalışırken hatanın nasıl görüldüğü önemli; tray tıklaması
			// sessiz kalmasın diye başarısızlığı bildir.
			e.notify("Kayıt başlatılamadı", err.Error())
			return StateIdle, "", err
		}
		return StateRecording, "", nil
	}
}

// start opens mic + Gemini and enters recording.
func (e *Engine) start(ctx context.Context) error {
	if err := e.ReloadConfig(); err != nil {
		return err
	}
	cfg := e.Config()
	// Non-empty language selects the translate model with the given target.
	session := gemini.New(cfg.APIKey, cfg.Language, gemini.ModeFromConfig(cfg.Transcription))
	if err := session.Start(ctx); err != nil {
		return err
	}
	if err := e.rec.Start(cfg.Microphone); err != nil {
		session.Cancel(ctx)
		return err
	}

	e.mu.Lock()
	e.sess = session
	e.state = StateRecording
	e.preview = ""
	hook := e.hook
	e.mu.Unlock()

	if hook != nil {
		hook(StateRecording)
	}
	return nil
}

// stopAndFinalize halts the mic, waits for the last word and produces text.
func (e *Engine) stopAndFinalize(ctx context.Context) (string, error) {
	e.mu.Lock()
	sess := e.sess
	e.state = StateFinishing
	e.mu.Unlock()

	e.rec.Mark() // tail capture begins
	time.Sleep(700 * time.Millisecond)
	if tail := e.rec.SinceMark(); len(tail) > 0 {
		sess.Push(tail)
	}
	e.rec.Stop()

	text, err := sess.Finish(ctx)

	e.mu.Lock()
	// Bu oturum hâlâ aktifse (arasında cancel→yeni kayıt başlamadıysa)
	// geçişi yap; aksi halde yeni oturumu ezmemek için dokunma.
	hadHook := false
	if e.sess == sess {
		e.sess = nil
		e.state = StateIdle
		e.preview = ""
		hadHook = e.hook != nil
	}
	hook := e.hook
	e.mu.Unlock()

	if hadHook {
		hook(StateIdle)
	}

	if err != nil {
		switch {
		case err == gemini.ErrCancelled:
			return "", err
		default:
			e.notify("Dikte hatası", err.Error())
			return "", err
		}
	}

	cfg := e.Config()
	final := dictionary.Apply(text, resToPairs(cfg.Dictionary))

	if pasteErr := e.paste(final); pasteErr != nil {
		if copyErr := copyToClipboard(final); copyErr != nil {
			e.notify("Yapıştırma ve kopyalama başarısız", final)
		} else {
			e.notify("Panoya kopyalandı", "Yapıştırılamadı — manuel yapıştır: "+short(final))
		}
	}
	return final, nil
}

// Cancel discards the running session with no paste and no error report.
// Pressed during finalize it aborts the pending result too.
func (e *Engine) Cancel(ctx context.Context) {
	e.mu.Lock()
	st := e.state
	sess := e.sess
	switch st {
	case StateRecording:
		e.state = StateIdle
		e.sess = nil
		e.preview = ""
	case StateFinishing:
		// stopAndFinalize kendi oturumunu serbest bırakacak; biz sadece
		// sonucu nakin hale getiriyoruz.
	}
	hook := e.hook
	e.mu.Unlock()

	switch st {
	case StateRecording:
		if hook != nil {
			hook(StateIdle)
		}
		if sess != nil {
			sess.Cancel(ctx)
		}
		e.rec.Stop()
	case StateFinishing:
		if sess != nil {
			sess.Cancel(ctx) // Finish ErrCancelled döndürür → paste atlanır
		}
	}
}

// resToPairs converts stored dictionary pairs, tolerating nil.
func resToPairs(src []config.Pair) []dictionary.Pair {
	out := make([]dictionary.Pair, 0, len(src))
	for _, p := range src {
		out = append(out, dictionary.Pair{Match: p.Match, Replace: p.Replace})
	}
	return out
}

// short trims long texts for notification bodies.
func short(s string) string {
	const max = 120
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// PasteWithTools exposes the injector chain for CLI testing.
func PasteWithTools(text string) error { return pasteWithTools(text) }

// notifySend shells out to the desktop notification daemon.
func notifySend(summary, body string) {
	cmd := exec.Command("notify-send", "-a", "Dictatego", "-t", "4000", summary, body)
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
}

// copyToClipboard falls back to wl-copy/wl-clipboard's tooling.
func copyToClipboard(text string) error {
	cmd := exec.Command("wl-copy", text)
	return cmd.Run()
}

// pasteUnavailable marks total failure of the injector chain.
type pasteUnavailable struct{}

func (pasteUnavailable) Error() string { return "yapıştırma yapılamadı" }

// errPasteUnavailable anchors the paste-fallback chain when every tool
// is absent or unusable.
var errPasteUnavailable = pasteUnavailable{}
