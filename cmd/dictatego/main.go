// Command dictatego is the tray dictation app. Phase 1 exposes CLI
// commands for wiring the audio and transcription layers:
//
//	dictatego devices           list capture devices
//	dictatego key               print whether an API key is configured
//	dictatego record [device]   talk, Ctrl+C to stop, prints the transcript
//	dictatego run               full tray application (Phase 2)
//	dictatego version
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"time"

	"dictatego/internal/app"
	"dictatego/internal/audio"
	"dictatego/internal/config"
	"dictatego/internal/gemini"
	"dictatego/internal/ipc"
	"dictatego/internal/overlay"
	"dictatego/internal/tray"
)

const version = "0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]

	cfg, err := config.Load()
	fatal(err)

	switch cmd {
	case "version":
		fmt.Println("dictatego", version)
	case "devices":
		list(rest)
	case "key":
		if cfg.APIKey == "" {
			fmt.Println("API anahtarı tanımlı değil")
			fmt.Println("-> config.json'a api_key alanını ekle veya GEMINI_API_KEY ortam değişkeni kullan")
			os.Exit(1)
		}
		fmt.Println("API anahtarı tanımlı ✔")
	case "record":
		record(cfg, rest)
	case "overlay":
		overlayDemo(rest)
	case "run":
		runDaemon()
	case "toggle", "stop", "cancel":
		sendAndReport(cmd)
	case "status":
		showStatus()
	case "paste":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "kullanım: dictatego paste \"metin\"")
			os.Exit(2)
		}
		fatal(app.PasteWithTools(rest[0]))
		fmt.Println("yapıştırma tamam")
	default:
		usage()
		os.Exit(2)
	}
}

func list(args []string) {
	rec := audio.NewRecorder(nil)
	defer rec.Close()
	devices, err := rec.Devices()
	fatal(err)
	// default first, then name order
	sort.Slice(devices, func(i, j int) bool { return devices[i].Label < devices[j].Label })
	fmt.Println("Mikrofonlar:")
	for _, d := range devices {
		fmt.Printf("  %s\t%s\n", d.Token, d.Label)
	}
}

// record is the end-to-end smoke test: mic → Gemini → transcript.
func record(cfg *config.Config, args []string) {
	key := cfg.APIKey
	if env := os.Getenv("GEMINI_API_KEY"); env != "" {
		key = env
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "API anahtarı gerekli (GEMINI_API_KEY)")
		os.Exit(1)
	}
	device := ""
	if len(args) > 0 {
		device = args[0]
	}

	ctx := context.Background()
	session := gemini.New(key, "", gemini.ModeFromConfig(cfg.Transcription))
	if err := session.Start(ctx); err != nil {
		fatal(err)
	}

	rec := audio.NewRecorder(session.Push)
	if err := rec.Start(device); err != nil {
		fatal(err)
	}
	defer rec.Close()
	fmt.Println("🎤 Kayıt başladı — Ctrl+C ile bitir")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	// tail capture, like the reference: wait briefly for the last word
	rec.Mark()
	time.Sleep(700 * time.Millisecond)
	if tail := rec.SinceMark(); len(tail) > 0 {
		session.Push(tail)
	}
	rec.Stop()

	fmt.Println("⏳ Metin bekleniyor...")
	text, err := session.Finish(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Println()
	fmt.Println("📝", text)
}

func usage() {
	fmt.Fprintln(os.Stderr, `kullanım:
  dictatego devices           mikrofonları listele
  dictatego key               API anahtarı durumunu göster
  dictatego record [device]   konuş → Ctrl+C → transkript yazdır (daemon'sız test)
  dictatego run               daemon olarak çalış (exec-once ile başlat)
  dictatego toggle            daemon: kayd başlat/bitir-yapıştır
  dictatego stop              daemon: bitir ve yapıştır
  dictatego cancel            daemon: kaydı at
  dictatego status            daemon durumu
  dictatego overlay [metin]   altyazı bar gösterim testi (deneysel, Wayland)
  dictatego paste "metin"    odak pencereye metin yazar (test)
  dictatego version`)
}

// runDaemon starts the IPC daemon: Hyprland binds then plain execs.
func runDaemon() {
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	engine := app.New(cfg)
	path, err := ipc.SocketPath()
	fatal(err)

	// Tray: gri ikon + menü. Menü tıklaması toggle'a bağlanır.
	tr := tray.Start(
		func() { state, _, _ := engine.Toggle(context.Background()); _ = state },
		func() { os.Exit(0) },
	)
	engine.SetStateHook(func(st app.State) {
		tr.SetActive(st == app.StateRecording)
	})

	fmt.Println("dictatego daemon:", path)
	handler := func(req ipc.Request) (ipc.Response, error) {
		ctx := context.Background()
		switch req.Cmd {
		case "toggle":
			state, text, err := engine.Toggle(ctx)
			resp := ipc.Response{OK: err == nil, State: string(state), Text: text}
			if err != nil && err != gemini.ErrCancelled {
				resp.Err = err.Error()
			}
			return resp, nil
		case "stop":
			state, text, err := engine.Toggle(ctx)
			resp := ipc.Response{OK: err == nil, State: string(state), Text: text}
			if err != nil && err != gemini.ErrCancelled {
				resp.Err = err.Error()
			}
			return resp, nil
		case "cancel":
			engine.Cancel(ctx)
			return ipc.Response{OK: true, State: string(app.StateIdle)}, nil
		case "status":
			stat, preview := engine.State()
			return ipc.Response{OK: true, State: string(stat), Text: preview}, nil
		default:
			return ipc.Response{}, fmt.Errorf("bilinmeyen komut: %s", req.Cmd)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		os.Exit(0)
	}()
	fatal(ipc.Serve(path, handler))
}

// engineStop removed: engine.Toggle already finalizes when recording.

func sendAndReport(cmd string) {
	resp, err := ipc.Call(ipc.Request{Cmd: cmd})
	if err != nil {
		fatal(err)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "hata: %s\n", resp.Err)
		os.Exit(1)
	}
	fmt.Printf("%s: %s\n", cmd, resp.State)
	if resp.Text != "" {
		fmt.Println(resp.Text)
	}
}

func showStatus() {
	resp, err := ipc.Call(ipc.Request{Cmd: "status"})
	if err != nil {
		fatal(err)
	}
	fmt.Println("durum:", resp.State)
	if resp.Text != "" {
		fmt.Println("önizleme:", resp.Text)
	}
}

// overlayDemo walks the bar through its states so the rendering can be
// checked on the real desktop.
func overlayDemo(args []string) {
	text := "Merhaba, bu Dictatego altlık barı"
	if len(args) > 0 {
		text = args[0]
	}
	ov, err := overlay.New()
	fatal(err)
	defer ov.Close()

	ov.Show(overlay.Recording, text)
	time.Sleep(2200 * time.Millisecond)
	ov.Show(overlay.Busy, "Google ile iletişim kuruluyor…")
	time.Sleep(1200 * time.Millisecond)
	ov.Show(overlay.Done, text)
	time.Sleep(1500 * time.Millisecond)
	ov.Show(overlay.ErrorState, "örnek hata mesajı görünümü")
	time.Sleep(1500 * time.Millisecond)
	ov.Hide()
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "hata:", err)
		os.Exit(1)
	}
}
