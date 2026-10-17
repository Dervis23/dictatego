// overlay package renders the transcription caption bar with
// wlr-layer-shell (Wayland) — a real desktop layer, no X11 needed.
package overlay

import (
	"os"
	"path/filepath"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Font search order: sans-serif faces with full Turkish (tr) coverage.
var fontCandidates = []string{
	"/usr/share/fonts/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/noto/NotoSans-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
}

// findFontFile returns the first existing candidate; on miss it scans
// /usr/share/fonts/**/*Sans-Regular.ttf as a generic fallback.
func findFontFile() (string, error) {
	for _, candidate := range fontCandidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	paths, _ := filepath.Glob("/usr/share/fonts/**/*Sans-Regular.ttf")
	paths2, _ := filepath.Glob("/usr/share/fonts/**/Sans*.ttf")
	for _, candidate := range append(paths, paths2...) {
		return candidate, nil
	}
	return "", os.ErrNotExist
}

// loadFace parses a system font into a scalable face.
func loadFace(size float64) (font.Face, error) {
	file, err := findFontFile()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	parsed, err := opentype.Parse(raw)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: size,
		DPI:  96,
	})
}

// measureText returns the pixel width of s under f.
func measureText(f font.Face, s string) int {
	return font.MeasureString(f, s).Ceil()
}

// elide cuts s until it fits inside maxWidth, appending an ellipsis.
// Cutting at rune boundaries keeps Turkish characters intact.
func elide(f font.Face, s string, maxWidth int) string {
	if s == "" {
		return s
	}
	if measureText(f, s) <= maxWidth {
		return s
	}
	runes := []rune(s)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi) / 2
		if measureText(f, string(runes[:mid])+"…") <= maxWidth {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return "…"
	}
	return string(runes[:lo]) + "…"
}

var _ = fixed.I // keep math/fixed imported for face plumbing symmetry
