# Dictatego — Plan

`ruwiss/gemini_speech` (Python, PyQt6) uygulamasının Go dilinde yeniden yazımı.
Kapsam: **yalnızca ana uygulama** (API anahtarlı v1.3.1 akışı); `cookie-fallback/`
yazılmayacak. Öncelikli hedef: **Hyprland / Sway (wlroots) + Wayland-native**.

Referans kaynak: `reference/` (klonlu, dokunma — salt okunur referans).

---

## 1. Kararlar

| Konu | Karar |
|---|---|
| Kapsam | Ana uygulama (tray dikte + Gemini Live API + yapıştır + ayarlar + overlay) |
| GUI | Native & minimal: tray + Wayland/X11 overlay, hafif ayar penceresi, tek binary |
| Öncelik | Hyprland / Sway (wlroots) önce; KDE ikinci faz; GNOME Viewer son faz |
| Platform | Linux/Wayland tam seviye önce, sonra X11, Windows/macOS ayrı faz |
| Referans repo | Analiz tamamlandı: `plan/rapor-referans.md` |

## 2. Mimari

Tek Go module (`dictatego`), tek binary:

```
cmd/dictatego/          # entry point: tray döngüsü, durum makinesi
internal/
  audio/                # mikrofon capture: 16kHz mono s16le (miniaudio/malgo: ALSA+PulseAudio)
  gemini/               # Gemini Live API WS istemcisi (JSON mesajlar, websocket lib)
  hotkey/               # global kısayol katmanı: Portal GlobalShortcuts → sway IPC → evdev
  overlay/              # altyazı bar: wlr-layer-shell + shm + freetype metin render
                        # X11'de: override-redirect pencere + strut
  paste/                # metin yapıştırma katmanı:
                        #   1) zwp_virtual_keyboard_v1 (Wayland-native Ctrl+V)
                        #   2) ydotool/wtype fallback
                        #   3) portal RemoteDesktop (KDE ve acil durum)
                        #   4) X11 XTest
  clipboard/            # wlr-data-control protokolü (wl-copy ile aynı iş)
  tray/                 # StatusNotifierItem (SNI) DBus tray + menü
  settings/             # ayar penceresi (Gio ile — Wayland/X11 native, tek binary)
  config/               # config.json (0600, API key dahil; keyring opsiyonu son faz)
  dictionary/           # kelime değiştirme motoru (referanstaki regex mantığının aynısı)
  update/               # GitHub release kendini güncelleme
  platform/             # linux/macos/windows ayırıcıları, build tag ile
```

Zaman uyumsuz model: Go goroutine + kanal.
Mikrofon `audio → gemini → overlay` boru hattı; kısayol events tek kanaldan.
Durum makinesi: `idle → recording → finishing → pasting` (+ `error`).

## 3. Wayland-native iyileştirmeler (Python'daki eksiklere karşı)

| Python'daki eksik | Dictatego çözümü |
|---|---|
| Overlay yalnızca KDE/Hyprland'da tam, diğerlerinde tahmini | `wlr-layer-shell` overlay surface: Hyprland, Sway, Nile, Wayfire, KWin hepsinde gerçek overlay |
| pynput X11'e dayalı, saf Wayland'da kısayol yok | xdg-desktop-portal `GlobalShortcuts` API (Hyprland portalında hazır) → Sway IPC `bindsym` → doğrudan evdev okuma (input grubu varsa) |
| Paste `/dev/uinput` ve dış araçlara muhtaç | `zwp_virtual_keyboard_v1` ile Wayland-native klavye simülasyonu (uinput yetkisi istemez); ydotool/wtype sadece fallback |
| GNOME tray görünmez | SNI tray + compositor-agnostik; GNOME'da AppIndicator DBus adapter |
| Self-update `/opt` yazma hatası | update script yazar ama yazma izni yoksa kullanıcıya net hata mesajı |
| API key düz metin, config yarım | config dosyası `0600`, ayar penceresinden key doğrulama hitap |

## 4. Fazlar

### Faz 1 — İskelet (Linux/Wayland olmazsa da çalışır)	✅ TAMAMLANDI
- [x] Go module kurulumu (go 1.25), `cmd/dictatego` giriş
- [x] config: yükleme/kaydetme (`~/.config/dictatego/config.json`, 0600)
- [x] gemini: Live API WS istemcisi — setup/transcribe modu, `realtimeInput` PCM,
      `audioStreamEnd`, interim+final akış, 10 dk limit (referans `speech.py` davranışlarıyla birebir)
- [x] audio: miniaudio (malgo) mikrofon kayıt, 16k/mono/s16le, RMS seviyesi + sessizlik kuyruk tespiti
- [x] CLI mod: `dictatego devices` cihaz listesi, `dictatego record` uçtan uca test — **canlı doğrulandı**
- Not: Live API yanıtları Binary frame ile geliyor; pump text+binary ikisini de çözer.

### Faz 2 — CLI daemon + Hyprland entegrasyonu (yeniden tasarım: GUI'siz)
Karar 2026-10-17: Ayar penceresi/tray/overlay KALDIRILDI. Mimari: config.json +
Hyprland `bind` satırları + notify-send. Overlay kodu `internal/overlay` yedeğinde
bekliyor (bind sorunu çözülürse opsiyonel olur).

- [x] ipc: unix socket daemon ($XDG_RUNTIME_DIR/dictatego.sock) + JSON komut protokolü (testli)
- [x] engine: durum makinesi (idle→recording→finishing) + toggle/stop/cancel/status
- [x] cli: `run` (daemon), `toggle`, `stop`, `cancel`, `status`, `paste` komutları
- [x] notify: notify-send ile durum/hata bildirimleri (engine içinde)
- [x] tray: StatusNotifierItem (fyne.io/systray, saf DBus) — "Kaydı Başlat/Durdur" +
      "Çıkış" menüsü — kullanıcı ortamında doğrulandı ✔
      (Not: Linux'ta gerçek kayıt systray.Run() event loop'unda; Register() yetmez)
      İkon (kullanıcı seçimi): boşta minik gri daire (#9e9e9e, 16px),
      kayıtta turuncu kare (#ff9800, 20×20)
3. Kullanıcı kendi Hyprland bind'larını kendisi ayarladı. Karar 2026-10-17:
   Hyprland 0.55+ Lua config'e geçti (~/.config/hypr/hyprland.lua) — README +
   install.sh çıktılarında `hl.bind("ALT + Z", hl.dsp.exec_cmd("dictatego toggle"))`
   formatı veriliyor; eski hyprlang `.conf` birkaç sürüm daha geçerli.
- [x] ayar komutları: `set language|mode|style|microphone` + `get` + `dict add|remove|list`
      (kullanıcı config.json elle düzenlemiyor; engine her kayıtta config'i yeniden okur)
- [x] çeviri modu daemon üzerinden (config'den language okur) — engine artık
      cfg.Language'ı gemini.New'a geçiyor; dil doluysa translate modeli seçilir.
      Canlı konuşma testi kullanıcı isteğiyle atlandı.

### Faz 3 — Metin yapıştırma (GUI'siz çekirdek tamamlayıcı)
- [x] paste: kullanıcı sisteminde hazır araçlar saptandı (wtype + wl-copy; ydotool daemon kapalı → sadece yedek)
- [x] paste zinciri: ① `wtype <metin>` (pano bozulmadan literal yazım) → ② `wl-copy` + wtype Ctrl+V → ③ ydotool Ctrl+V → hepsi düşerse panoya kopyala + Haber
- [ ] kendi wlr istemcisi (data-control + virtual keyboard): araçlar her yerde yoksa; şart değil, yedekte

### Faz 4 — UX / özellik tamamlama
- [x] config komutları: `dictatego set language|mode|style|microphone` + `get` + `dict add|remove|list`
- [x] Dictionary motoru devreye alındı: ✔ (Faz 1'de tamamlandı, testli; `dict` komutuyla CLI'dan yönetilir)
- [ ] self-update: GitHub release + checksum doğrulama
- [ ] hold-to-talk: daemon.tabi? basılı tutma modu (config.style=hold)
- [ ] single-instance: socket zaten çakışmayı engeller

### Faz 5 — Paketleme & diğer OS
- [ ] AUR PKGBUILD, `install.sh` (`~/.local` kurulum + .desktop + symlink)
- [ ] systemd user unit / hyprland exec-once önerisi
- [ ] Windows/macOS derlemeler (ayrı faz, Wayland yoktur)

### Faz 6 — Test & polish
- [x] unit: gemini istemcisi, dictionary, config (kısmen)
- [ ] entegrasyon: Hyprland + Sway gerçek manuel test listesi (README'de tablo)
- [ ] Performans: boşta < %0.5 CPU, kayıtta < %3 CPU, RAM < 40 MB

## 5. Açık konular (ilerlerken karar verilecek)

- Tray: `getlantern/systray` (GTK) yerine sıfırdan SNI DBus istemcisi mi? → Faz 2'de prototip
- Ayar penceresi Gio mu, yoksa `zenity`/portal tabanlı hafif çözüm mü?
- Gemini transcribe model adı güncel sürümde değişmiş olabilir (v1beta ws) — Faz 1'de Google dökümanıyla doğrulanacak.
- Wayland overlay'de font render: freetype (cgo) ya da `golang.org/x/image/font/sfnt` (saf Go) — saf Go tercih.
