// Package gemini implements the Gemini Live API transcription client.
//
// Behaviour mirrors reference/gemini_speech/speech.py:
//   - the WebSocket endpoint authenticates with ?key=<api key>,
//   - audio flows as base64 PCM s16le 16 kHz mono in ~100 ms chunks,
//   - SMART/VERBATIM transcription, or live translation into a language,
//   - a 10-minute per-session cap,
//   - on finish: 120 ms of silence + audioStreamEnd, then the client waits
//     for the text to settle (the translate model is flushed by pacing
//     real-time silence into the stream),
//   - the API key is redacted from every error string.
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"
)

const (
	Host   = "generativelanguage.googleapis.com"
	Path   = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	Rate   = 16000
	Width  = 2    // bytes per sample, s16le mono
	Chunk  = 3200 // 100 ms of PCM
	maxPCM = 10 * 60 * Rate * Width
	minPCM = Rate * Width / 5
	MIME   = "audio/pcm;rate=16000"

	setupTimeout  = 15 * time.Second
	finalTimeout  = 1500 * time.Millisecond
	translateHold = 6 * time.Second
	// WarmLimit is how long an opened but unused session stays reusable.
	WarmLimit = 8 * time.Minute
)

// User-visible messages (reference wording).
const (
	MsgMissingKey  = "Add a Gemini API key in Settings"
	MsgNoSpeech    = "No speech detected"
	MsgTooShort    = "Recording is too short"
	MsgTooLong     = "Live transcription stops at 10 minutes"
	MsgFailed      = "Transcription failed"
	MsgSetupFailed = "Live setup failed"
)

// ErrCancelled reports that the user aborted the run.
var ErrCancelled = errors.New("cancelled")

// Mode selects the transcription style.
type Mode string

const (
	Smart    Mode = "SMART"
	Verbatim Mode = "VERBATIM"
)

// ModeFromConfig maps the stored config value to a protocol mode.
func ModeFromConfig(transcription string) Mode {
	if strings.EqualFold(transcription, "verbatim") {
		return Verbatim
	}
	return Smart
}

// Result is the outcome of one session.
type Result struct {
	Text string
	Err  error // ErrCancelled means the user aborted without an error
}

// Session is one live run. Concurrent use is safe:
//   - Push from the audio goroutine,
//   - Preview from the UI goroutine,
//   - Start/Finish/Cancel from any goroutine.
type Session struct {
	key      string
	language string
	mode     Mode

	audio chan []byte      // PCM chunks; nil is the end-of-stream sentinel
	msgs  chan readOutcome // reader goroutine → run goroutine
	done  chan Result      // exactly one result delivered by run

	started bool
	opened  time.Time // set once setupComplete arrives

	wsMu     sync.Mutex
	ws       *websocket.Conn
	wsClosed bool

	stateMu sync.Mutex
	finals  []string
	interim string
	translated strings.Builder
	cancelled  bool
	limitHit   bool

	// Owned by the run goroutine only:
	streamErr error
	completed bool
	sent      int
	heardIn   time.Time
	heardOut  time.Time
}

// New creates a session. A non-empty language selects the translate model;
// otherwise it transcribes with the given mode.
func New(key, language string, mode Mode) *Session {
	return &Session{
		key:      strings.TrimSpace(key),
		language: strings.TrimSpace(language),
		mode:     mode,
		audio:    make(chan []byte, 64),
		msgs:     make(chan readOutcome, 128),
		done:     make(chan Result, 1),
	}
}

// Start dials the API, performs the setup handshake and launches the
// pumping goroutines. The context governs the session's lifetime.
func (s *Session) Start(ctx context.Context) error {
	if s.started {
		return errors.New("session already started")
	}
	if s.key == "" {
		return errors.New(MsgMissingKey)
	}
	ws, err := dial(ctx, s.key)
	if err != nil {
		return errors.New(redact(errText(err), s.key))
	}
	s.ws = ws
	// The reader must be alive before the handshake: otherwise the
	// setupComplete reply has nobody to carry it into s.msgs.
	go s.pump(ctx)

	if err := s.write(ctx, setupMessage(s.language, s.mode)); err != nil {
		s.shutdown(ctx)
		detail := redact(errText(err), s.key)
		if detail == "" {
			detail = MsgSetupFailed
		}
		return errors.New(detail)
	}
	setup, detail, ok := s.read(ctx, setupTimeout)
	if detail != "" {
		s.shutdown(ctx)
		return errors.New(redact(detail, s.key))
	}
	if !ok || setup == nil {
		s.shutdown(ctx)
		return errors.New(MsgSetupFailed)
	}
	if _, has := setup["setupComplete"]; !has {
		s.shutdown(ctx)
		return errors.New(redact(failureMessage(setup), s.key))
	}
	s.opened = time.Now()

	s.started = true
	go s.run(ctx) // audio → socket, transcript bookkeeping
	return nil
}

// Push queues one PCM chunk. It never blocks the caller: if the send side
// stalls, the oldest chunk is dropped so live latency stays bounded.
func (s *Session) Push(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	blob := append([]byte(nil), pcm...)
	select {
	case s.audio <- blob:
		return
	default:
	}
	select {
	case <-s.audio:
	default:
	}
	select {
	case s.audio <- blob:
	default:
	}
}

// Finish signals end-of-stream and waits for the final result.
func (s *Session) Finish(ctx context.Context) (string, error) {
	if !s.started {
		return "", errors.New("session not started")
	}
	select {
	case s.audio <- nil:
	default:
	}
	select {
	case res := <-s.done:
		return res.Text, res.Err
	case <-ctx.Done():
		return s.Preview(), ctx.Err()
	}
}

// Cancel aborts the session. The final result becomes "" + ErrCancelled.
func (s *Session) Cancel(ctx context.Context) {
	s.stateMu.Lock()
	s.cancelled = true
	s.stateMu.Unlock()
	select {
	case s.audio <- nil:
	default:
	}
	s.shutdown(ctx)
}

// Warm reports whether a session with no transcript yet is still reusable
// for a follow-up recording without a fresh handshake. Pre-opened warm
// sessions keep first-word latency low, like the reference.
func (s *Session) Warm() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.started || s.cancelled {
		return false
	}
	if len(s.finals) > 0 || s.interim != "" || s.translated.Len() > 0 {
		return false
	}
	return !s.opened.IsZero() && time.Since(s.opened) <= WarmLimit
}

// Preview returns the text produced so far.
func (s *Session) Preview() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return composeText(s.finals, s.interim, &s.translated, s.language != "")
}

func composeText(finals []string, interim string, translated *strings.Builder, isTranslation bool) string {
	if isTranslation {
		return strings.Join(strings.Fields(translated.String()), " ")
	}
	parts := make([]string, 0, len(finals)+1)
	for _, piece := range finals {
		if p := strings.TrimSpace(piece); p != "" {
			parts = append(parts, p)
		}
	}
	if interim = strings.TrimSpace(interim); interim != "" {
		parts = append(parts, interim)
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------- run loop

// run pumps audio into the socket and drives the transcript bookkeeping.
// It owns almost all mutable session state (protected by stateMu where
// the UI goroutine reads it).
func (s *Session) run(ctx context.Context) {
	result := s.loop(ctx)

	if s.isCancelled() {
		result = Result{Err: ErrCancelled}
	}
	s.done <- result
	s.shutdown(ctx)
}

func (s *Session) loop(ctx context.Context) Result {
	var pending []byte
	ending := false
	endSeen := false

	for {
		if s.isCancelled() || ctx.Err() != nil {
			return Result{}
		}
		s.drainTick(ctx, 0)
		if s.streamErr != nil {
			return Result{Err: s.streamErr}
		}

		blob, eof, got := s.takeAudio()
		if got {
			if eof {
				endSeen = true
			} else {
				pending = append(pending, blob...)
			}
		}
		if endSeen {
			ending = true
		}

		for len(pending) > 0 && (ending || len(pending) >= Chunk) {
			if s.sent >= maxPCM {
				s.limitHit = true
				pending = pending[:0]
				ending = true
				break
			}
			size := len(pending)
			if !ending && size > Chunk {
				size = Chunk
			}
			if s.sent+size > maxPCM {
				size = maxPCM - s.sent
			}
			if err := s.sendAudio(ctx, pending[:size]); err != nil {
				return Result{Err: err}
			}
			pending = pending[size:]
		}

		if !ending {
			continue
		}
		if s.sent < minPCM {
			return Result{Err: errors.New(MsgTooShort)}
		}
		if s.language != "" {
			s.collectTranslation(ctx)
			return s.finishResult()
		}
		if err := s.sendAudio(ctx, make([]byte, Rate*Width*120/1000)); err != nil {
			return Result{Err: err}
		}
		if err := s.write(ctx, map[string]any{"realtimeInput": map[string]any{"audioStreamEnd": true}}); err != nil {
			return Result{Err: err}
		}
		s.collectFinal(ctx)
		return s.finishResult()
	}
}

// finishResult packages what the text pipeline produced after the stream ended.
func (s *Session) finishResult() Result {
	text := s.Preview()
	if text != "" {
		return Result{Text: text}
	}
	if s.streamErr != nil {
		return Result{Err: s.streamErr}
	}
	if s.limitHit {
		return Result{Err: errors.New(MsgTooLong)}
	}
	return Result{Err: errors.New(MsgNoSpeech)}
}

// takeAudio pulls the next chunk, coalescing everything immediately
// available. It never blocks long: with nothing queued it waits 20 ms.
func (s *Session) takeAudio() (blob []byte, eof, got bool) {
	var first []byte
	select {
	case first = <-s.audio:
	case <-time.After(20 * time.Millisecond):
		return nil, false, false
	}
	if first == nil {
		return nil, true, true
	}
	for {
		select {
		case extra := <-s.audio:
			if extra == nil {
				return first, true, true
			}
			first = append(first, extra...)
		default:
			return first, false, true
		}
	}
}

// collectFinal waits for the server's generationComplete (it usually
// follows audioStreamEnd by 0.3–0.7 s), falling back to text settling:
// a pause before stopping can complete the turn early.
func (s *Session) collectFinal(ctx context.Context) {
	s.completed = false
	started := time.Now()
	changed := started
	var last string
	for time.Since(started) < finalTimeout && !s.isCancelled() {
		s.drainTick(ctx, 40*time.Millisecond)
		if s.completed {
			tail := time.Now().Add(120 * time.Millisecond)
			for time.Now().Before(tail) && !s.isCancelled() {
				s.drainTick(ctx, 40*time.Millisecond)
			}
			return
		}
		text := s.Preview()
		if text != last {
			last = text
			changed = time.Now()
		}
		if time.Since(started) >= 900*time.Millisecond && time.Since(changed) >= 300*time.Millisecond {
			return
		}
	}
}

// collectTranslation paces silence at real-time rate until the translated
// text settles. The translate model does not flush on audioStreamEnd and
// never sends a completion signal, so the reference waits like this too.
func (s *Session) collectTranslation(ctx context.Context) {
	unit := time.Duration(Chunk) * time.Second / (Rate * Width) // 100 ms
	started := time.Now()
	nextSend := started

	for {
		now := time.Now()
		if s.isCancelled() || s.streamErr != nil || now.Sub(started) >= translateHold {
			return
		}
		if !now.Before(nextSend) {
			if s.sent+Chunk > maxPCM {
				return
			}
			if err := s.sendAudio(ctx, make([]byte, Chunk)); err != nil {
				return
			}
			nextSend = nextSend.Add(unit)
		}
		wait := nextSend.Sub(now)
		if wait > 50*time.Millisecond {
			wait = 50 * time.Millisecond
		} else if wait <= 0 {
			wait = 0
		}
		s.drainTick(ctx, wait)

		now = time.Now()
		s.stateMu.Lock()
		in, out := s.heardIn, s.heardOut
		s.stateMu.Unlock()
		if in.IsZero() {
			// A short phrase takes about 2 s to produce its transcript.
			if now.Sub(started) >= 4*time.Second {
				return
			}
			continue
		}
		quiet := now.Sub(in)
		if out.After(in) {
			quiet = now.Sub(out)
		}
		caughtUp := !out.Before(in)
		if now.Sub(started) >= 1200*time.Millisecond && caughtUp && quiet >= time.Second {
			return
		}
	}
}

// --------------------------------------------------------- message handling

// drainTick applies every message already available; if none has arrived
// yet it waits up to timeout for the first one.
func (s *Session) drainTick(ctx context.Context, timeout time.Duration) {
	got := false
	for {
		var (
			outc   readOutcome
			received bool
		)
		if got {
			select {
			case outc, received = <-s.msgs:
			default:
				return
			}
		} else {
			guard := time.NewTimer(timeout)
			select {
			case outc, received = <-s.msgs:
				guard.Stop()
			case <-guard.C:
				return
			}
		}
		if !received {
			return
		}
		if outc.err != nil {
			if s.streamErr == nil {
				s.streamErr = outc.err
			}
			return
		}
		if outc.msg != nil {
			s.apply(outc.msg)
			got = true
		}
	}
}

// apply folds one server message into the transcript state.
func (s *Session) apply(msg map[string]any) {
	if _, isErr := msg["error"]; isErr {
		if s.streamErr == nil {
			s.streamErr = errors.New(redact(failureMessage(msg), s.key))
		}
		return
	}
	content, _ := msg["serverContent"].(map[string]any)
	if content == nil {
		return
	}
	if content["generationComplete"] != nil || content["turnComplete"] != nil {
		s.completed = true
	}

	if s.language != "" {
		// Translated text arrives as pieces, each with its own leading space.
		if in, _ := content["inputTranscription"].(map[string]any); in != nil {
			if text, _ := in["text"].(string); text != "" {
				s.heardIn = time.Now()
			}
		}
		if out, _ := content["outputTranscription"].(map[string]any); out != nil {
			if piece, _ := out["text"].(string); piece != "" {
				s.stateMu.Lock()
				s.translated.WriteString(piece)
				s.stateMu.Unlock()
				s.heardOut = time.Now()
			}
		}
		return
	}

	interim, _ := content["interimInputTranscription"].(map[string]any)
	final, _ := content["inputTranscription"].(map[string]any)
	s.stateMu.Lock()
	if text, _ := interim["text"].(string); strings.TrimSpace(text) != "" {
		s.interim = text
	}
	if text, _ := final["text"].(string); strings.TrimSpace(text) != "" {
		text = strings.TrimSpace(text)
		if n := len(s.finals); n > 0 && strings.HasPrefix(text, s.finals[n-1]) {
			s.finals[n-1] = text
		} else {
			s.finals = append(s.finals, text)
		}
		// The final can stop short of the interim it replaces: keep the tail.
		s.interim = remainder(s.interim, text)
	}
	s.stateMu.Unlock()
}

// remainder keeps the part of the interim the final did not cover yet,
// comparing only when the final's words line up with the interim's start.
func remainder(interim, final string) string {
	words := strings.Fields(strings.TrimSpace(interim))
	done := strings.Fields(final)
	if len(words) <= len(done) {
		return ""
	}
	for i := range done {
		if plainWord(words[i]) != plainWord(done[i]) {
			return ""
		}
	}
	return strings.Join(words[len(done):], " ")
}

// plainWord lowercases and strips every non-alphanumeric character, like
// the reference's comparison (Unicode-aware).
func plainWord(word string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(word) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- socket IO

// readOutcome is one decoded server message or a fatal read error.
type readOutcome struct {
	msg map[string]any
	err error
}

// pump reads the socket until it closes, decoding each JSON message.
// A persistent reader avoids dropping half-read frames on timeouts.
func (s *Session) pump(ctx context.Context) {
	defer close(s.msgs)
	for {
		kind, data, err := s.ws.Read(ctx)
		if err != nil {
			if ce := (websocket.CloseError{}); errors.As(err, &ce) && ce.Reason != "" {
				err = fmt.Errorf("%s", redact(ce.Reason, s.key))
			} else if ctx.Err() != nil {
				err = nil // orderly teardown: not a failure
			}
			if err != nil {
				s.msgs <- readOutcome{err: redactErrText(err, s.key)}
			}
			return
		}
		// The Live API answers with BINARY frames even for JSON payloads —
		// both text and binary must decode below (the hand-rolled reference
		// client accepted opcodes 1 and 2 alike).
		if kind != websocket.MessageText && kind != websocket.MessageBinary {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			s.msgs <- readOutcome{err: errors.New(MsgFailed)}
			return
		}
		select {
		case s.msgs <- readOutcome{msg: payload}:
		case <-ctx.Done():
			return
		}
	}
}

// read fetches one message with a timeout. ok=false covers a timeout and
// a read failure; detail carries the redacted failure text when known.
func (s *Session) read(ctx context.Context, timeout time.Duration) (msg map[string]any, detail string, ok bool) {
	guard := time.NewTimer(timeout)
	defer guard.Stop()
	select {
	case outc := <-s.msgs:
		if outc.err != nil {
			return nil, outc.err.Error(), false
		}
		if outc.msg == nil {
			return nil, detail, false
		}
		return outc.msg, "", true
	case <-guard.C:
		return nil, "", false
	}
}

func (s *Session) write(ctx context.Context, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	return s.ws.Write(ctx, websocket.MessageText, data)
}

// sendAudio streams one PCM blob as base64 in a realtimeInput message.
func (s *Session) sendAudio(ctx context.Context, pcm []byte) error {
	s.sent += len(pcm)
	return s.write(ctx, map[string]any{
		"realtimeInput": map[string]any{
			"audio": map[string]any{
				"data":     base64.StdEncoding.EncodeToString(pcm),
				"mimeType": MIME,
			},
		},
	})
}

// shutdown closes the socket exactly once, unblocking the pump.
func (s *Session) shutdown(ctx context.Context) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.ws == nil || s.wsClosed {
		return
	}
	s.wsClosed = true
	if ctx.Err() == nil {
		_ = s.ws.Close(websocket.StatusNormalClosure, "")
	} else {
		s.ws.CloseNow()
	}
}

// ---------------------------------------------------------------- helpers

func dial(ctx context.Context, key string) (*websocket.Conn, error) {
	endpoint := fmt.Sprintf("wss://%s%s?key=%s", Host, Path, url.QueryEscape(key))
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": []string{"dictatego/1.0"}},
	}
	ws, resp, err := websocket.Dial(ctx, endpoint, opts)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("%s", resp.Status)
		}
		return nil, err
	}
	return ws, nil
}

// setupMessage mirrors the reference exactly.
func setupMessage(language string, mode Mode) map[string]any {
	if language != "" {
		return map[string]any{
			"setup": map[string]any{
				"model": "models/gemini-3.5-live-translate-preview",
				"generationConfig": map[string]any{
					"responseModalities": []string{"AUDIO"},
					"translationConfig": map[string]any{
						"targetLanguageCode": language,
						"echoTargetLanguage": true,
					},
				},
				"inputAudioTranscription":  map[string]any{},
				"outputAudioTranscription": map[string]any{},
			},
		}
	}
	if mode != Verbatim {
		mode = Smart
	}
	return map[string]any{
		"setup": map[string]any{
			"model":            "models/gemini-3.5-transcribe-live",
			"generationConfig": map[string]any{"responseModalities": []string{"TEXT"}},
			"inputAudioTranscription": map[string]any{"mode": mode},
		},
	}
}

// failureMessage extracts the API's human-readable error text.
func failureMessage(msg map[string]any) string {
	if msg == nil {
		return MsgFailed
	}
	errObj, _ := msg["error"].(map[string]any)
	if errObj != nil {
		if text, _ := errObj["message"].(string); text != "" {
			return text
		}
		if status, _ := errObj["status"].(string); status != "" {
			return status
		}
	}
	return MsgFailed
}

func errText(err error) string { return err.Error() }

func redactErrText(err error, key string) error {
	return errors.New(redact(errText(err), key))
}

// redact removes the API key from any message and collapses whitespace.
func redact(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "")
	}
	return strings.Join(strings.Fields(text), " ")
}

func (s *Session) isCancelled() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.cancelled
}
