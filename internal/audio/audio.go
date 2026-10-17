// Package audio captures microphone input.
//
// It mirrors reference/gemini_speech/audio.py where it matters for output
// quality: 16 kHz / mono / s16le PCM chunks, silence padding when a late
// driver would splice audio together, mark/since-mark tail capture and an
// RMS level helper used by the tail/stop logic.
//
// The capture backend is miniaudio (via malgo), which talks to ALSA and
// PulseAudio/PipeWire directly — no Qt toolkit dependency.
package audio

import (
	"math"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	Rate        = 16000
	Width       = 2
	PeriodInMs  = 15
	PeriodCount = 3
)

// Device is one capture device: Token is what Settings persists.
type Device struct {
	Token string
	Label string
}

// Recorder opens and owns the microphone.
type Recorder struct {
	ctx *malgo.AllocatedContext
	dev *malgo.Device

	sink func([]byte)

	mu    sync.Mutex
	chunk Bytes // captured data
	mark  int   // byte offset recorded by the last Mark call
	total int
	heard time.Time
	running bool
}

// Bytes keeps the recording; it is plain concatenation of emitted chunks.
type Bytes [][]byte

// NewRecorder creates an idle recorder; InitContext runs on first Start.
func NewRecorder(sink func([]byte)) *Recorder {
	return &Recorder{sink: sink}
}

// Start opens the device ("" = default) and begins calling sink with PCM.
func (r *Recorder) Start(deviceToken string) error {
	if r.ctx == nil {
		ctx, err := malgo.InitContext(
			[]malgo.Backend{malgo.BackendPulseaudio, malgo.BackendAlsa},
			malgo.ContextConfig{},
			func(message string) {},
		)
		if err != nil {
			return err
		}
		r.ctx = ctx
	}
	if r.dev != nil {
		r.dev.Stop()
		r.dev.Uninit()
		r.dev = nil
	}

	id, err := r.findDevice(deviceToken)
	if err != nil {
		return err
	}
	config := malgo.DefaultDeviceConfig(malgo.Capture)
	config.SampleRate = Rate
	config.PeriodSizeInMilliseconds = PeriodInMs
	config.Periods = PeriodCount
	config.PerformanceProfile = malgo.LowLatency
	config.Capture = malgo.SubConfig{
		Channels: 1,
		Format:   malgo.FormatS16,
	}
	if id != nil {
		config.Capture.DeviceID = id.Pointer()
	}

	onData := r.onData
	dev, err := malgo.InitDevice(r.ctx.Context, config, malgo.DeviceCallbacks{Data: onData})
	if err != nil {
		return err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return err
	}
	r.dev = dev
	r.resetState()
	return nil
}

// Stop ceases the capture. The device object is closed: miniaudio keeps an
// internal queue we would never sync with anyway.
func (r *Recorder) Stop() {
	r.mu.Lock()
	r.running = false
	dev, ctx := r.dev, r.ctx
	r.dev = nil
	r.chunk = nil
	r.mark = 0
	r.heard = time.Time{}
	r.mu.Unlock()
	if dev != nil {
		dev.Stop()
		dev.Uninit()
	}
	_ = ctx
}

// Mark remembers the current end of the captured stream.
func (r *Recorder) Mark() {
	r.mu.Lock()
	r.mark = r.total
	r.mu.Unlock()
}

// SinceMark returns the data captured after the last Mark call.
func (r *Recorder) SinceMark() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mark >= r.total {
		return nil
	}
	var out []byte
	across := 0
	for _, chunk := range r.chunk {
		start := 0
		if r.mark > across {
			start = r.mark - across
		}
		if start < len(chunk) {
			out = append(out, chunk[start:]...)
		}
		across += len(chunk)
	}
	return out
}

// Active reports whether capture is running.
func (r *Recorder) Active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Close releases the audio context entirely (call on app shutdown).
func (r *Recorder) Close() {
	if r.ctx != nil {
		r.ctx.Uninit()
		r.ctx.Free()
		r.ctx = nil
	}
}

// Devices lists available capture devices.
func (r *Recorder) Devices() ([]Device, error) {
	if r.ctx == nil {
		ctx, err := malgo.InitContext(
			[]malgo.Backend{malgo.BackendPulseaudio, malgo.BackendAlsa},
			malgo.ContextConfig{},
			func(message string) {},
		)
		if err != nil {
			return nil, err
		}
		r.ctx = ctx
	}
	infos, err := r.ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(infos))
	for i := range infos {
		info := &infos[i]
		token := info.ID.String()
		if token == "" {
			continue
		}
		label := strings.TrimSpace(info.Name())
		if label == "" {
			label = token
		}
		devices = append(devices, Device{Token: token, Label: label})
	}
	return devices, nil
}

func (r *Recorder) findDevice(token string) (*malgo.DeviceID, error) {
	if token == "" {
		return nil, nil // default device
	}
	infos, err := r.ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		if infos[i].ID.String() == token {
			id := infos[i].ID
			return &id, nil
		}
	}
	return nil, nil // the saved device may be unplugged; use the default
}

// resetState clears the capture buffers for a new recording.
func (r *Recorder) resetState() {
	r.mu.Lock()
	r.chunk = nil
	r.mark = 0
	r.total = 0
	r.heard = time.Time{}
	r.running = true
	r.mu.Unlock()
}

// onData runs on miniaudio's audio thread. Keep it short and allocation-light.
func (r *Recorder) onData(_, pInput []byte, _ uint32) {
	if len(pInput) == 0 {
		return
	}
	data := append([]byte(nil), pInput...) // the buffer is reused by miniaudio

	r.mu.Lock()
	now := time.Now()
	if !r.heard.IsZero() {
		// A late driver hands back less audio than the time that passed.
		// Silence keeps the clock, so the lost stretch is not spliced together.
		late := now.Sub(r.heard).Seconds() - float64(len(data))/float64(Rate*Width)
		if late > 0.06 {
			samples := int(math.Min(late, 0.4) * Rate)
			if samples > 0 {
				r.emitLocked(make([]byte, samples*Width))
			}
		}
	}
	r.heard = now
	r.emitLocked(data)
	r.running = true
	r.mu.Unlock()

	if r.sink != nil {
		r.sink(data)
	}
}

func (r *Recorder) emitLocked(data []byte) {
	r.chunk = append(r.chunk, data)
	r.total += len(data)
}

// Level computes the RMS of s16le PCM (the reference's stop-tail metric).
func Level(pcm []byte) int {
	if len(pcm) < 2 {
		return 0
	}
	var total float64
	count := 0
	for i := 0; i+1 < len(pcm); i += 2 * 4 { // stride 8 bytes like the reference
		sample := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		total += float64(sample) * float64(sample)
		count++
	}
	if count == 0 {
		return 0
	}
	return int(math.Sqrt(total / float64(count)))
}
