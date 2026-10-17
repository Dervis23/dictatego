package overlay

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	"golang.org/x/image/font"
)

// Overlay is the transcription caption bar anchored to the bottom edge of
// the compositor's work area. It is a real wlr-layer-shell surface: it
// never steals focus, stacks above normal windows and works identically
// on Hyprland, Sway, KWin, Wayfire, niri and river.
type Overlay struct {
	display    *client.Display
	compositor *client.Compositor
	shm        *client.Shm
	shell      *LayerShell
	output     *client.Output
	surface    *client.Surface
	layer      *LayerSurface

	face       font.Face
	phase      int
	configured bool
	canvas     *canvas

	mu    sync.Mutex // guards state/text
	state State
	text  string

	redraw   chan struct{}
	exit     chan struct{}
	loopDone chan struct{}
}

func New() (*Overlay, error) {
	o := &Overlay{
		redraw:   make(chan struct{}, 1),
		exit:     make(chan struct{}),
		loopDone: make(chan struct{}),
	}

	display, err := client.Connect("")
	if err != nil {
		return nil, fmt.Errorf("Wayland'a bağlanılamadı: %w", err)
	}
	o.display = display

	display.SetErrorHandler(func(e client.DisplayErrorEvent) {
		log.Fatalf("compositor protokol hatası: object=%d code=%d msg=%q",
			e.ObjectId, e.Code, e.Message)
	})

	registry, err := display.GetRegistry()
	if err != nil {
		return nil, err
	}
	registry.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
		switch e.Interface {
		case "wl_compositor":
			o.compositor = client.NewCompositor(display.Context())
			_ = registry.Bind(e.Name, e.Interface, e.Version, o.compositor)
		case "wl_shm":
			o.shm = client.NewShm(display.Context())
			_ = registry.Bind(e.Name, e.Interface, e.Version, o.shm)
		case "wl_output":
			if o.output == nil {
				o.output = client.NewOutput(display.Context())
				_ = registry.Bind(e.Name, e.Interface, e.Version, o.output)
			}
		case "zwlr_layer_shell_v1":
			o.shell = NewLayerShell(display.Context())
			_ = registry.Bind(e.Name, e.Interface, e.Version, o.shell)
		}
	})
	o.roundtrip()
	o.roundtrip()

	if o.compositor == nil || o.shm == nil || o.shell == nil {
		_ = display.Context().Close()
		return nil, fmt.Errorf("compositor wlr-layer-shell desteklemiyor")
	}

	if face, ferr := loadFace(fontSize); ferr == nil {
		o.face = face
	} else {
		log.Printf("font bulunamadı (%v) — metinsiz bar", ferr)
	}

	surface, err := o.compositor.CreateSurface()
	if err != nil {
		return nil, err
	}
	o.surface = surface

	layer, err := o.shell.GetLayerSurface(surface, o.output, uint32(LayerShellLayerTop), "dictatego")
	if err != nil {
		return nil, err
	}
	o.layer = layer

	// Bottom edge, full width; keyboard never lands on us.
	_ = layer.SetAnchor(uint32(LayerSurfaceAnchorBottom | LayerSurfaceAnchorLeft | LayerSurfaceAnchorRight))
	_ = layer.SetExclusiveZone(-1) // float over panels too
	_ = layer.SetKeyboardInteractivity(uint32(LayerSurfaceKeyboardInteractivityNone))
	// 16 px beside the bottom edge, mirroring the reference's Wayland feel.
	_ = layer.SetMargin(0, 0, 16, 0)

	layer.SetConfigureHandler(o.onConfigure)

	// First commit: the compositor answers with a configure event.
	_ = surface.Attach(nil, 0, 0)
	_ = surface.Commit()

	go o.loop()
	return o, nil
}

// roundtrip waits for one compositor sync cycle (used during setup only).
func (o *Overlay) roundtrip() {
	cb, err := o.display.Sync()
	if err != nil {
		return
	}
	done := false
	cb.SetDoneHandler(func(client.CallbackDoneEvent) { done = true })
	for !done {
		if err := o.display.Context().Dispatch(); err != nil {
			return
		}
	}
	_ = cb.Destroy()
}

// onConfigure sizes the canvas and presents the current frame.
func (o *Overlay) onConfigure(e LayerSurfaceConfigureEvent) {
	w := int32(e.Width)
	h := int32(e.Height)
	if w <= 0 {
		w = 480
	}
	if h <= 0 {
		h = 64
	}

	if o.canvas != nil && (o.canvas.w != w || o.canvas.h != h) {
		o.canvas.destroy()
		o.canvas = nil
	}
	if o.canvas == nil {
		c, err := newCanvas(o.shm, w, h)
		if err != nil {
			log.Printf("overlay canvas: %v", err)
			return
		}
		o.canvas = c
	}
	o.configured = true
	_ = o.layer.AckConfigure(e.Serial)

	o.mu.Lock()
	state, text := o.state, o.text
	o.mu.Unlock()
	o.present(state, text)
}

// present draws and commits one frame (dispatch goroutine only).
func (o *Overlay) present(state State, text string) {
	if !o.configured || o.canvas == nil {
		return
	}
	o.phase++
	idx, ok := o.canvas.paintInto(func(img *imageRGBA) {
		drawFrame(img, state, text, o.face, o.phase)
	})
	if !ok {
		return // both buffers busy; skip this stale frame
	}
	_ = o.surface.Attach(o.canvas.buffer(idx), 0, 0)
	_ = o.surface.Commit()
}

// ---------------------------------------------------------------- public API

// Show displays the bar in the requested state with the given caption.
func (o *Overlay) Show(state State, text string) {
	o.mu.Lock()
	o.state, o.text = state, text
	o.mu.Unlock()
	o.poke()
}

// SetText updates the caption in place.
func (o *Overlay) SetText(text string) {
	o.mu.Lock()
	o.text = text
	o.mu.Unlock()
	o.poke()
}

// SetState updates the state in place.
func (o *Overlay) SetState(state State) {
	o.mu.Lock()
	o.state = state
	o.mu.Unlock()
	o.poke()
}

// Hide removes the bar.
func (o *Overlay) Hide() {
	o.mu.Lock()
	o.state = Hidden
	o.mu.Unlock()
	o.poke()
}

func (o *Overlay) poke() {
	select {
	case o.redraw <- struct{}{}:
	default:
	}
	// Doorbell: a Sync reply guarantees the dispatch loop wakes up even
	// when the compositor has nothing else to send.
	if o.display != nil {
		if cb, err := o.display.Sync(); err == nil {
			cb.SetDoneHandler(func(_ client.CallbackDoneEvent) { _ = cb.Destroy() })
		}
	}
}

// Close tears down the window and waits for the loop.
func (o *Overlay) Close() {
	select {
	case <-o.exit:
		// already closed
	default:
		close(o.exit)
	}
	select {
	case <-o.loopDone:
	case <-time.After(2 * time.Second):
	}
}

// ---------------------------------------------------------------- loop

func (o *Overlay) loop() {
	defer close(o.loopDone)
	for {
		select {
		case <-o.exit:
			o.teardown()
			return
		default:
		}
		select {
		case <-o.redraw:
			o.mu.Lock()
			state, text := o.state, o.text
			o.mu.Unlock()
			o.present(state, text)
			continue // drain the next command before blocking again
		default:
		}
		if err := o.display.Context().Dispatch(); err != nil {
			o.teardown()
			return
		}
	}
}

func (o *Overlay) teardown() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.canvas != nil {
		o.canvas.destroy()
		o.canvas = nil
	}
	if o.layer != nil {
		_ = o.layer.Destroy()
	}
	if o.surface != nil {
		_ = o.surface.Destroy()
	}
	if o.display != nil {
		_ = o.display.Context().Close()
	}
}

// imageRGBA names the render target type once, so other files keep depending
// only on the standard image package.
type imageRGBA = imageRGBAImpl
