package overlay

import (
	"image"
	"image/color"
	"math"
	"os"
	"sync"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"
)

const (
	barPadding = 28
	dotRadius  = 7
	fontSize   = 15
	barRadius  = 14
	buffersN   = 2
	panelA     = 217 // panel alpha in 1/255: ~0.85
)

// State selects the visual mode of the bar.
type State int

const (
	Hidden State = iota
	Recording
	Busy
	Done
	ErrorState
)

// stateColor is the accent colour of each state.
func (s State) color() (byte, byte, byte) {
	switch s {
	case Recording, ErrorState:
		return 240, 78, 82 // red
	case Busy:
		return 78, 158, 240 // blue
	case Done:
		return 62, 207, 110 // green
	default:
		return 160, 160, 165
	}
}

// canvas owns an shm pool with double buffers.
type canvas struct {
	mu       sync.Mutex
	w, h     int32
	stride   int32
	pool     *client.ShmPool
	file     *os.File
	data     []byte
	buffers  []*client.Buffer
	views    []*image.RGBA
	released [buffersN]bool
}

func newCanvas(shm *client.Shm, w, h int32) (*canvas, error) {
	stride := w * 4
	span := stride * h
	total := int(span) * buffersN

	file, err := os.CreateTemp("", "dictatego-shm-")
	if err != nil {
		return nil, err
	}
	if err := file.Truncate(int64(total)); err != nil {
		file.Close()
		return nil, err
	}
	data, err := unix.Mmap(int(file.Fd()), 0, total, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		file.Close()
		return nil, err
	}

	pool, err := shm.CreatePool(int(file.Fd()), int32(total))
	if err != nil {
		unix.Munmap(data)
		file.Close()
		return nil, err
	}

	c := &canvas{w: w, h: h, stride: stride, pool: pool, file: file, data: data}
	c.buffers = make([]*client.Buffer, buffersN)
	c.views = make([]*image.RGBA, buffersN)

	for i := 0; i < buffersN; i++ {
		idx := i
		buf, err := pool.CreateBuffer(int32(idx)*span, w, h, stride, uint32(client.ShmFormatArgb8888))
		if err != nil {
			c.destroy()
			return nil, err
		}
		c.buffers[idx] = buf
		buf.SetReleaseHandler(func(_ client.BufferReleaseEvent) {
			c.mu.Lock()
			c.released[idx] = true
			c.mu.Unlock()
		})
		offset := idx * int(span)
		c.views[idx] = &image.RGBA{
			Pix:    data[offset : offset+int(span)],
			Stride: int(stride),
			Rect:   image.Rect(0, 0, int(w), int(h)),
		}
		c.released[idx] = true // nothing presented yet
	}
	return c, nil
}

// paintInto picks the next released buffer, renders the frame into it and
// reports the buffer index for Attach. ok=false means both buffers are
// still on screen — a stale frame is skipped; the next update catches up.
func (c *canvas) paintInto(draw func(*image.RGBA)) (idx int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < buffersN; i++ {
		if c.released[i] {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, false
	}
	view := c.views[idx]
	for i := range view.Pix {
		view.Pix[i] = 0
	}
	draw(view)
	c.released[idx] = false
	return idx, true
}

func (c *canvas) buffer(idx int) *client.Buffer { return c.buffers[idx] }

func (c *canvas) destroy() {
	if c.pool != nil {
		c.pool.Destroy()
		c.pool = nil
	}
	for _, b := range c.buffers {
		if b != nil {
			b.Destroy()
		}
	}
	if c.data != nil {
		unix.Munmap(c.data)
		c.data = nil
	}
	if c.file != nil {
		c.file.Close()
		c.file = nil
	}
}

// ---------------------------------------------------------------- drawing

// imageRGBAImpl is the frame paint target: an alias to image.RGBA over
// the shm memory-mapped bytes.
type imageRGBAImpl = image.RGBA

// drawFrame publishes the whole bar: translucent rounded panel, state dot
// and the caption text (or a red explanation row on errors).
func drawFrame(img *image.RGBA, state State, text string, face font.Face, phase int) {
	const paddingX = 24

	// Translucent rounded panel in premultiplied ARGB.
	panel := color.RGBA{R: 28, G: 30, B: 34, A: panelA}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if insideRoundedRect(x, y, w, h, barRadius) {
				img.Set(x, y, panel)
			}
		}
	}

	if state == ErrorState {
		drawText(img, "hata: "+text, face, color.RGBA{R: 255, G: 130, B: 130, A: 255}, paddingX)
		return
	}

	r, g, b := state.color()
	centre := image.Point{X: paddingX + dotRadius, Y: h / 2}
	switch state {
	case Recording:
		// Pulse: radius breathes with the phase.
		pulse := dotRadius + (phase % 3)
		fillCircle(img, centre, pulse, r, g, b)
	case Busy:
		// Spinner: four orbiting dots around the accent centre.
		for i := 0; i < 4; i++ {
			angle := float64(phase+i) * 0.35
			px := centre.X + int(11*math.Cos(angle))
			py := centre.Y + int(11*math.Sin(angle))
			fillCircle(img, image.Point{X: px, Y: py}, 2, r, g, b)
		}
	case Done:
		fillCircle(img, centre, dotRadius, r, g, b)
		drawCheck(img, centre)
	default:
		fillCircle(img, centre, dotRadius, r, g, b)
	}

	textX := paddingX + dotRadius*3 + 6
	drawText(img, text, face, color.RGBA{R: 255, G: 255, B: 255, A: 255}, textX)
}

func insideRoundedRect(x, y, w, h, r int) bool {
	if r <= 0 {
		return true
	}
	cx := clamp(x, r, w-1-r)
	cy := clamp(y, r, h-1-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fillCircle paints a circle with hard edges (fine at 7px scale).
func fillCircle(img *image.RGBA, c image.Point, radius int, r, g, b byte) {
	for y := c.Y - radius - 1; y <= c.Y+radius+1; y++ {
		if y < 0 || y >= img.Rect.Dy() {
			continue
		}
		for x := c.X - radius - 1; x <= c.X+radius+1; x++ {
			if x < 0 || x >= img.Rect.Dx() {
				continue
			}
			dx, dy := float64(x-c.X)+0.5, float64(y-c.Y)+0.5
			if dx*dx+dy*dy <= float64(radius*radius) {
				putOpaque(img, x, y, r, g, b)
			}
		}
	}
}

// drawCheck places a small white tick in the circle's middle.
func drawCheck(img *image.RGBA, c image.Point) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for k := -2; k <= 2; k++ {
		img.Set(c.X+k, c.Y, white)
		img.Set(c.X+k, c.Y+1, white)
	}
}

func putOpaque(img *image.RGBA, x, y int, r, g, b byte) {
	idx := img.PixOffset(x, y)
	img.Pix[idx], img.Pix[idx+1], img.Pix[idx+2], img.Pix[idx+3] = b, g, r, 255
}

// drawText paints s at baseline-fitted position starting at x, eliding overflow.
func drawText(img *image.RGBA, s string, face font.Face, col color.RGBA, x int) {
	if face == nil || s == "" {
		return
	}
	metrics := face.Metrics()
	maxWidth := img.Rect.Dx() - x - barPadding
	if maxWidth < 48 {
		maxWidth = 48
	}
	safe := elide(face, s, maxWidth)
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(premulForText(col)),
		Face: face,
		Dot: fixed.Point26_6{
			X: fixed.I(x),
			Y: fixed.I((img.Rect.Dy() + int(metrics.Ascent)/2) / 2),
		},
	}
	d.DrawString(safe)
}

func premulForText(c color.RGBA) color.RGBA {
	a := float64(c.A) / 255
	return color.RGBA{
		R: byte(float64(c.R) * a),
		G: byte(float64(c.G) * a),
		B: byte(float64(c.B) * a),
		A: c.A,
	}
}
