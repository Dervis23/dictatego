# Dictatego

Wayland (wlroots / Hyprland / Sway) diktasyon uygulaması. Kısayol tuşuna bas →
konuş → transkript **odaklı pencereye yapıştırılır**. Tek binary, tray ikonu,
daemon mimarisi.



## Nasıl çalışır

```
[bind] ─exec─▶ dictatego toggle ─▶ unix socket ─▶ daemon
                                       │
                          mikrofon (miniaudio/ALSA, 16kHz s16le)
                                       │  ~3.2KB'lık base64 parçalar
                          Gemini Live API (websocket, binary frame)
                                       │  transkript (yapılandırılmış)
                          sözlük düzeltmesi → wtype ile odak pencereye
```

- **IPC daemon**: `run` komutuyla başlar, diğer komutlar socket üzerinden ayar
  yapar. Çift başlatma engellenir, sıra bozuk socket temizlenir.
- **Tray ikonu** (`fyne.io/systray`, saf DBus SNI): boşta **gri daire**,
  kayıtta **turuncu kare**. Menü: Kaydı Başlat/Durdur, Çıkış.
- **Yapıştırma**: Wayland `virtual-keyboard` protokolü üzerinden `wtype`
  birebir yazar; olmazsa `wl-copy` + Ctrl+V, en sonda `ydotool`. Metin
  her durumda panoya da kopyalanır.

## Kurulum

```bash
./packaging/install.sh --user          # ~/.local/bin
# ya da sistem geneli:
sudo ./packaging/install.sh
```

Önkoşullar: `go` (>= 1.22), runtime'da `wtype`, `wl-clipboard`, bir bildirim
sunucusu (`libnotify`). `ydotool` opsiyoneldir (daemonı çalışmalı).

## Kullanım

| Komut | İş |
|---|---|
| `dictatego run` | daemon + tray başlat (`exec-once = dictatego run`) |
| `dictatego toggle` | kayıt başlat / bitir-yapıştır |
| `dictatego stop` / `cancel` | bitir / at |
| `dictatego status` | durum + canlı önizleme metni |
| `dictatego paste "metin"` | odak pencereye metin yaz (test) |
| `dictatego set language tr` | konuştuğun dil **çeviri** moduna geçer (dil kodu) |
| `dictatego set language off` | normal transkripsiyona döner |
| `dictatego set mode verbatim` | `smart` (doğal) / `verbatim` (birebir) |
| `dictatego set microphone <ad>` | `devices` çıktısındaki cihaz adı |
| `dictatego get` | ayar özeti |
| `dictatego dict add "klasör" <yazım>` | sözlük: adlandırılan kelime düzeltmesi |
| `dictatego devices` | mikrofonları listele |
| `dictatego key` | API anahtarı durumunu göster |
| `dictatego record` | daemon'sız hızlı test |

### Hyprland örnek bind — Lua konfig (0.55+)

`~/.config/hypr/hyprland.lua`:

```lua
-- daemon + tray: otomatik başlat
hl.exec_cmd("dictatego run")

-- kayıt başlat/bitir-yapıştır
hl.bind("ALT + Z", hl.dsp.exec_cmd("dictatego toggle"))

-- iptal
hl.bind("ALT + X", hl.dsp.exec_cmd("dictatego cancel"))
```

> Not: Hyprland 0.55'ten beri konfig Lua'dır (`hyprland.lua`); eski
> `hyprlang` `.conf` yalnızca birkaç sürüm daha çalışır, geçirmen önerilir:
> `bind = ALT, Z, exec, dictatego toggle` (eski `.conf` karşılığı).

### Sway (hyprlang tabanlı sway config)

```
exec dictatego run
bindsym $mod+z exec dictatego toggle
bindsym $mod+x exec dictatego cancel
```

### systemd (opsiyonel)

`packaging/dictatego.service` user unit; `--systemd` seçeneğiyle kurılır:

```bash
systemctl --user enable --now dictatego.service
```

## Ayarlar & API anahtarı

Config: `~/.config/dictatego/config.json` (**0600** izinli — içinde API
anahtarı var). Alanlar referans uygulamayla uyumlu:

```json
{
  "api_key": "AIza...",
  "language": "",
  "transcription": "smart",
  "style": "toggle",
  "microphone": "",
  "dictionary": [{"match": "klasör", "replace": "Yapılandır"}]
}
```

Elle düzenlemek yerine `dictatego set` / `dict` komutlarını kullanın.

## Geliştirme

```bash
go test ./... && go vet ./...
go build -o dictatego ./cmd/dictatego
```

Yol haritası ve mimari kararlar: [PLAN.md](PLAN.md).
park: `internal/overlay` — ekran üstü altyazı çubuğu prototipi (go-wayland
kütüphane hatası yüzünden beklemede).
