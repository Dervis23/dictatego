#!/usr/bin/env bash
# dictatego installer — builds and installs the binary.
# Usage:
#   ./install.sh            # system-wide: /usr/local/bin (sudo gerektirir)
#   ./install.sh --user     # kullanıcı bazlı: ~/.local/bin
#   ./install.sh --systemd  # (kullanıcı kurulumuyla beraber) systemd user unit
set -euo pipefail

PREFIX="/usr/local"
WITH_SYSTEMD=0
for arg in "$@"; do
  case "$arg" in
    --user)    PREFIX="$HOME/.local" ;;
    --systemd) WITH_SYSTEMD=1 ;;
    *) echo "bilinmeyen seçenek: $arg"; exit 2 ;;
  esac
done

command -v go >/dev/null || { echo "go derleyicisi gerekli"; exit 1; }

echo "==> derleniyor..."
go build -trimpath -ldflags "-s -w" -o dictatego ./cmd/dictatego

echo "==> kuruluyor: ${PREFIX}/bin/dictatego"
mkdir -p "${PREFIX}/bin"
install -m 0755 dictatego "${PREFIX}/bin/dictatego"
rm -f ./dictatego

if [[ "$PREFIX" = "$HOME/.local" ]] ; then
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) ;;
    *) echo "not: $HOME/.local/bin PATH'te değil — shell rc dosyana ekle" ;;
  esac
fi

cat <<EOF

== Kurulum tamam ==

Hyprland configine şunu ekle (ya da systemd unit kullan):

  exec-once = dictatego run
  # kayıt için:
  bind = ALT, Z, exec, dictatego toggle
  # iptal için:
  bind = ALT, X, exec, dictatego cancel

Ayar için:
  dictatego set language tr    # çeviri modu
  dictatego set mode verbatim  # birebir transkripsiyon
  dictatego set microphone <ad>  # devices komutuyla gör
  dictatego dict add "Gemini" "Yeni Gemini"

systemd user unit için: ./install.sh --user --systemd
EOF

if [[ "$WITH_SYSTEMD" = 1 ]]; then
  SRC="$(dirname "$0")/dictatego.service"
  DST="$HOME/.config/systemd/user/dictatego.service"
  mkdir -p "$(dirname "$DST")"
  if [[ "$PREFIX" = "/usr/local" ]]; then
    sed "s|%h/.local/bin|/usr/local/bin|" "$SRC" > "$DST"
  else
    cp "$SRC" "$DST"
  fi
  echo "==> unit yazıldı: $DST"
  echo "    systemctl --user daemon-reload && systemctl --user enable --now dictatego.service"
fi
