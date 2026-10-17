// Package tray renders the system tray icon via StatusNotifierItem
// (pure DBus, no GTK): gray when idle, red while recording, plus a
// small menu — mirroring the reference app's own tray.
package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"fyne.io/systray"
)

// Colors match the reference's painted tray dot.
var (
	idleColor   = color.RGBA{R: 154, G: 160, B: 170, A: 255} // #9aa0aa
	activeColor = color.RGBA{R: 240, G: 78, B: 82, A: 255}   // #f04e52
)

// Handler connects the tray to the engine.
type Handler struct {
	onToggle func()
	onQuit   func()

	toggleItem *systray.MenuItem
	lastActive bool
}

// Start runs the tray icon loop in a background goroutine (Run's native
// event loop performs the actual SNI registration on Linux).
func Start(onToggle, onQuit func()) *Handler {
	h := &Handler{onToggle: onToggle, onQuit: onQuit}
	go systray.Run(h.onReady, h.onExit)
	return h
}

func (h *Handler) onReady() {
	h.toggleItem = systray.AddMenuItem("Kaydı Başlat/Durdur", "Toggle dikte")
	go func() {
		for range h.toggleItem.ClickedCh {
			if h.onToggle != nil {
				h.onToggle()
			}
		}
	}()
	systray.AddSeparator()
	quit := systray.AddMenuItem("Çıkış", "Dictatego daemon'ı kapat")
	go func() {
		for range quit.ClickedCh {
			if h.onQuit != nil {
				h.onQuit()
			}
		}
	}()

	systray.SetTooltip("Dictatego — dikte hazır")
	systray.SetIcon(dotIcon(false))
}

func (h *Handler) onExit() {}

// SetActive swaps the icon: gray idle ⇄ red recording.
func (h *Handler) SetActive(active bool) {
	if h == nil || h.lastActive == active {
		return
	}
	h.lastActive = active
	systray.SetIcon(dotIcon(active))
	if active {
		systray.SetTooltip("Dictatego — KAYIT ALIYOR")
	} else {
		systray.SetTooltip("Dictatego — dikte hazır")
	}
}

// dotIcon paints a 32×32 filled circle like the reference's QPainter dot.
func dotIcon(active bool) []byte {
	c := idleColor
	if active {
		c = activeColor
	}
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			// Antialiased edge via a simple coverage ramp.
			dx, dy := float64(x)-15.5, float64(y)-15.5
			dist := dx*dx + dy*dy
			switch {
			case dist <= 144: // radius 12 fully inside
				img.Set(x, y, c)
			case dist <= 272: // fade to radius ~16.5
				t := (272 - dist) / 128
				faded := color.RGBA{R: c.R, G: c.G, B: c.B, A: uint8(float64(c.A) * t)}
				img.Set(x, y, faded)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
