# Referans Analiz Raporu — ruwiss/gemini_speech

Kaynak: `reference/` klonu (okuma amaçlı). Bu rapor yeniden yazımın temel girdisidir.

## Uygulama ne yapar?

Sistem tepsili dictate uygulaması: global kısayol (varsayılan `Alt+Z`) → mikrofon →
Gemini API transkript → metni aktif pencereye yapıştır. İkinci kısayol canlı çeviri
(15 dil). Özellikler: SMART/VERBATIM modları, mikrofon seçimi, ayarlanabilir
kısayollar, sözlük (kelime değiştirme), 10 dk oturum limiti, altyazı overlay bar,
kendi kendine güncelleme, clipboard fallback.

## Mimari (ana paket `gemini_speech/` v1.3.1)

| Dosya | İş |
|---|---|
| `app.py` (662 satır) | Tray (QSystemTrayIcon) + durum makinesi + ayar yükleme/kaydetme + sıcak oturum ısıtma + Esc-iptal polling (40ms) + update thread |
| `audio.py` (267) | PyQt6 QtMultimedia ile mikrofon, 16kHz/mono/Int16, 0.5s buffer, gecikme boşluk doldurma, RMS seviyesi |
| `speech.py` (498) | Elle yazılmış WebSocket + ssl + Gemini Live API (`wss://...BidiGenerateContent?key=`), realtimeInput base64 PCM (100ms parça), audioStreamEnd, SMART/VERBATIM setup, çeviri pacing, key redaction |
| `hotkey.py` (297) | Windows: RegisterHotKey mesaj döngüsü + GetAsyncKeyState. Diğer OS: pynput (X11 tabanlı!), elle combo state tracking |
| `desktop.py` (1038) | **Linux/Wayland kalbi**: KWin script (dbus-send), Hyprland socket IPC, paste zinciri: /dev/uinput ioctl → ydotool/wtype/xdotool/dotool → portal RemoteDesktop (libdbus ctypes) → X11 XTest |
| `indicator.py` (543) | Frameless overlay: animasyonlu kelime kelime altyazı, fade/crossfade, durum görselleri (kırmızı/mavi/yeşil/kırmızı-X) |
| `settings.py` (451) | Ayar dialog'u (koyu QSS): key, kısayol yakalama, dil, mod, mikrofon, sözlük editörü |
| `paste.py` | Platform yönlendirme: Linux→desktop.py; Win keybd_event, mac osascript |
| `dictionary.py` | normalize/apply: uzun eşleşme önce, case-insensitive word-boundary |
| `update.py` (174) | GitHub releases API kontrolü (sadece frozen), SHA-256 doğrulama, kurulum script üretimi |
| `paths.py`/`certs.py`/`crashlog.py`/`messages.py` | root/config dizinleri, CA bundle, crash log, kullanıcı mesajları |

`cookie-fallback/` (v1.0.0): eski mimari — Chrome extension cookie toplama →
Native Messaging host → yerel HTTP sunucu (127.0.0.1:4982) → Gemini dahili web
speech servisi (WebChannel long-polling + protobuf + SAPISIDHASH). ffmpeg gerekir.
**Kapsam dışı** (karar), içerdiği hardcoded API key nedeniyle de taşınmayacak.

## Ana veri akışı

```
hotkey event → app state machine
  → audio engine (QThread) push PCM → speech.LiveSession.push (queue)
      → WS setup + realtimeInput → interim/final transcript
  → overlay.show(dictionary.apply(preview))
stop → tail sessizlik bekleme (RMS<400, 0.7s) → finish (audioStreamEnd + 1.5-6s)
  → dict apply → paste → başarısızsa clipboard + "Copy text" tray öğesi
```

## Tespit edilen eksikler / kusurlar

### Wayland/Linux
1. **Overlay konumlandırma** KDE (KWin script) ve Hyprland özel; GNOME/Sway/wlroots
   tahmini alt-marj. layer-shell yok.
2. **Hotkey pynput → X11** tabanlı; saf Wayland'da sessizce çalışmayabilir.
   Esc-iptal de aynı dinleyiciye bağımlı.
3. **Paste kırılgan zincir**: /dev/uinput genellikle erişilemez (root/input grubu);
   ydotool daemon, wtype/xdotool/dotool kurulu değilse; portal RemoteDesktop
   onay gerektirir (~45s ilk onay) ve ilk cevapta UI thread bloklanır.
4. Tek-instance mantığı Windows-only; Linux'ta çift başlatma → hotkey çakışma pop-up.
5. Tray QSystemTrayIcon — GNOME'da (SNI host yoksa) görünmez.
6. Linux self-update: `/opt` (PKGBUILD) kurulumuna yazmayı dener → sessiz hata; PKGBUILD 1.0.9'a sabit (eski).
7. certs.py distro CA yollarını elle tahmin eder.

### Genel
8. API key düz metin config.json'da.
9. Çakışma tespiti (RegisterHotKey probe) Windows-only; diğer OS'ta geçersiz kısayol kaydedilebilir.
10. `paste.py _press()` Linux dalı yok (ulaşılamaz ama kırılgan).
11. `os._exit(0)` Qt destructor'larını atlar; `__import__("time")` gibi stil sorunları.
12. Overlay font 14px sabit — ölçekleme/high-DPI yok.
13. voice activity detection, seviye göstergesi, geçmiş/geçmişte ara gibi özellikler yok.
14. Translate modeli audioStreamEnd'i düzgün flush etmiyor — 6s pacing hack'i ile çözülüyor.

## Go yeniden yazımına etkiler

- audio: QtMultimedia → miniaudio (malgo): ALSA/PulseAudio, device enumeration
- hotkey: Portal GlobalShortcuts (Hyprland hazır) → sway IPC bindsym → evdev
- overlay: wlr-layer-shell + shm + saf Go font render (x/image/font/sfnt)
- paste: zwp_virtual_keyboard_v1 (uinput yetkisi gerektirmez!) → araçlar → portal → XTest
- clipboard: wlr-data-control
- tray: StatusNotifierItem DBus (GTK'sız)
- gemini: standart websocket lib, JSON mesaj protokolü (elle WS yazmıyoruz)
- Windows API çağrıları (mutex, RegisterHotKey, keybd_event) → golang.org/x/sys ile gerçek implementasyon
