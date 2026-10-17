package app

import (
	"os/exec"
	"time"
)

// pasteWithTools injects final text into the focused window.
//
// Chain (first success wins):
//  1. wtype <text>          — literal typing over the Wayland
//     virtual-keyboard protocol; the clipboard is left untouched and no
//     daemon/permission is required (Hyprland/Sway support this natively).
//  2. wl-copy + wtype ctrl+v — classic paste: text lands on the clipboard
//     (so the user can re-paste manually), then a synthetic Ctrl+V types it.
//  3. wl-copy + ydotool      — same keystroke via the ydotoold daemon.
//  4. On total failure the text stays on the clipboard; the caller reports
//     "Copied. Could not paste" style notifications.
func pasteWithTools(text string) error {
	if runTool("wtype", text) == nil {
		return nil
	}
	if err := copyToClipboard(text); err != nil {
		return err
	}
	// The compositor needs a beat between clipboard write and keystroke.
	time.Sleep(60 * time.Millisecond)
	if runTool("wtype", "-M", "ctrl", "-k", "v", "-m", "ctrl") == nil {
		return nil
	}
	if runTool("ydotool", "key", "29:1", "29:0") == nil {
		return nil
	}
	return errPasteUnavailable
}

// runTool runs an external injector, mirroring the reference's tool layer.
func runTool(name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return toolMissing{name}
	}
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

type toolMissing struct{ name string }

func (e toolMissing) Error() string { return e.name + " kurulu değil" }
