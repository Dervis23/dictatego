// Package tray renders the system tray icon via StatusNotifierItem
// (pure DBus, no GTK): gray when idle, red while recording, plus a
// small menu — mirroring the reference app's own tray.
package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"fyne.io/systray"
)

// Colors for the user's chosen tray scheme.
var (
	idleColor   = color.RGBA{R: 158, G: 158, B: 158, A: 255} // nötr gri #9e9e9e
	activeColor = color.RGBA{R: 255, G: 152, B: 0, A: 255}   // turuncu #ff9800
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

// dotIcon paints the user's state scheme on a 32×32 transparent canvas:
// inactive → tiny gray circle (16px diameter), recording → orange square
// (20×20, stop-button look).
func dotIcon(active bool) []byte {
	c := idleColor
	if active {
		c = activeColor
	}
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	paint := func(cov float64, x, y int) {
		if cov <= 0 || x < 0 || y < 0 || x >= 32 || y >= 32 {
			return
		}
		if cov > 1 {
			cov = 1
		}
		img.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: uint8(255 * cov)})
	}
	if !active {
		// minik daire: merkez (16,16), yarıçap 8, AA kenar
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				d := math.Hypot(float64(x)+0.5-16, float64(y)+0.5-16) - 8
				paint(0.5-d, x, y)
			}
		}
	} else {
		// turuncu kare: 20×20, merkez (16,16), 3px köşe yuvarlama
		const x0, y0, x1, y1, r = 6.0, 6.0, 26.0, 26.0, 3.0
		for y := 5; y <= 27; y++ {
			for x := 5; x <= 27; x++ {
				fx, fy := float64(x)+0.5, float64(y)+0.5
				cxr := math.Max(x0+r, math.Min(fx, x1-r))
				cyr := math.Max(y0+r, math.Min(fy, y1-r))
				d := math.Hypot(fx-cxr, fy-cyr) - r
				inside := fx >= x0 && fx <= x1 && fy >= y0 && fy <= y1
				cov := 0.0
				switch {
				case inside && d >= -0.5:
					cov = 0.5 - d
				case inside:
					cov = 1
				}
				paint(cov, x, y)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
