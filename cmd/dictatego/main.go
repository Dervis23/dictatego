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
	"strings"
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
	case "set":
		runSet(rest)
	case "get":
		runGet(cfg)
	case "dict":
		runDict(rest, cfg)
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
  dictatego paste "metin"     odak pencereye metin yazar (test)
  dictatego set <alan> <değer>
                              ayarları günceller: language, mode, style,
                              microphone (bir sonraki kayıtta geçerli)
  dictatego get               ayarların özeti
  dictatego dict add <öğe> <yazım>   sözlüğe kelime ekle
  dictatego dict remove <öğe>        sözlükten kelime çıkar
  dictatego dict list               sözlüğü listele
  dictatego version`)
}

// runSet updates one config field from the CLI ('set language off' clears).
func runSet(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "kullanım: dictatego set <language|mode|style|microphone> <değer>")
		os.Exit(2)
	}
	field := args[0]
	val := strings.Join(args[1:], " ")
	cfg, err := config.Load()
	fatal(err)
	switch field {
	case "language":
		if val == "off" || val == "none" {
			val = ""
		}
		cfg.Language = val
	case "mode", "transcription":
		switch val {
		case "smart", "verbatim":
			cfg.Transcription = val
		default:
			fmt.Fprintln(os.Stderr, "mode: 'smart' ya da 'verbatim' olmalı")
			os.Exit(2)
		}
	case "style":
		switch val {
		case "toggle", "hold":
			cfg.Style = val
		default:
			fmt.Fprintln(os.Stderr, "style: 'toggle' ya da 'hold' olmalı")
			os.Exit(2)
		}
	case "microphone":
		if val == "off" || val == "default" {
			val = ""
		}
		cfg.Microphone = val
	default:
		fmt.Fprintf(os.Stderr, "bilinmeyen alan: %s (language, mode, style, microphone)\n", field)
		os.Exit(2)
	}
	fatal(cfg.Save())
	fmt.Printf("%s = %q yazıldı — bir sonraki kayıtta geçerli\n", field, val)
}

// runGet prints a short settings summary (API key redacted).
func runGet(cfg *config.Config) {
	key := "yok"
	if cfg.APIKey != "" {
		key = "tanımlı"
	}
	fmt.Printf("api_key:       %s\n", key)
	fmt.Printf("language:      %q (çeviri %s)\n", cfg.Language, onOff(cfg.Language != ""))
	fmt.Printf("mode:          %s\n", cfg.Transcription)
	fmt.Printf("style:         %s\n", cfg.Style)
	fmt.Printf("microphone:    %q\n", cfg.Microphone)
	fmt.Printf("dictionary:    %d öğe\n", len(cfg.Dictionary))
}

func onOff(b bool) string {
	if b {
		return "açık"
	}
	return "kapalı"
}

// runDict manipulates dictionary pairs and persists them.
func runDict(args []string, cfg *config.Config) {
	if len(args) == 0 {
		runDictList(cfg)
		return
	}
	switch args[0] {
	case "list":
		runDictList(cfg)
	case "add":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "kullanım: dictatego dict add <öğe> <yazım>")
			os.Exit(2)
		}
		match, replace := args[1], strings.Join(args[2:], " ")
		for _, p := range cfg.Dictionary {
			if p.Match == match {
				fmt.Printf("zaten var: %q → %q (değiştirilmedi)\n", p.Match, p.Replace)
				return
			}
		}
		cfg.Dictionary = append(cfg.Dictionary, config.Pair{Match: match, Replace: replace})
		fatal(cfg.Save())
		fmt.Printf("eklendi: %q → %q\n", match, replace)
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "kullanım: dictatego dict remove <öğe>")
			os.Exit(2)
		}
		kept := cfg.Dictionary[:0]
		found := false
		for _, p := range cfg.Dictionary {
			if p.Match == args[1] {
				found = true
				continue
			}
			kept = append(kept, p)
		}
		if !found {
			fmt.Printf("yok: %q\n", args[1])
			return
		}
		n := len(kept)
		// slice reuse keeps capacity; rebuild to be safe with aliasing
		kept = append([]config.Pair(nil), kept...)
		cfg.Dictionary = kept
		fatal(cfg.Save())
		fmt.Printf("çıkarıldı: %q (kalan %d öğe)\n", args[1], n)
	default:
		fmt.Fprintln(os.Stderr, "kullanım: dictatego dict <list|add|remove> ...")
		os.Exit(2)
	}
}

func runDictList(cfg *config.Config) {
	if len(cfg.Dictionary) == 0 {
		fmt.Println("sözlük boş")
		return
	}
	for _, p := range cfg.Dictionary {
		fmt.Printf("%q → %q\n", p.Match, p.Replace)
	}
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
