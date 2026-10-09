#!/usr/bin/env bash
# Records the README demo: a terminal runs `kropka --lan` and a phone browser
# (Playwright) scans its QR code and browses the folder, then the two go side
# by side into out/demo.mp4 and out/demo.gif. `record.sh --stitch` redoes only
# that last step, from the recordings already in out/.
#
# The terminal runs in a container (VHS's image) on a Docker network of its
# own, so kropka sees a home-network address and prints that, and the folder
# is ~/Pictures/space. The phone runs here and opens that address.
#
# Needs: docker, go, node, ffmpeg; gifski makes a better GIF.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
cd "$here"

OUT=$here/out
IMAGE=ghcr.io/charmbracelet/vhs
NET=kropka-demo
SUBNET=${DEMO_SUBNET:-192.168.1.0/24}
IP=${DEMO_IP:-192.168.1.42}
export OUT KROPKA_URL="http://$IP:8080/?t=demo"

BG=0x0e0e10      # kropka's own background
TERM_BG=0x1e1e2e # the tape's theme (Catppuccin Mocha)
PAD=14           # around the terminal's text
GAP=16           # between and around the two halves

if [ "${1:-}" != --stitch ]; then # --stitch: only redo the video and GIF
  rm -rf "$OUT"
  mkdir -p "$OUT/bin" "$OUT/cache" "$OUT/home/Pictures"
  [ -d node_modules ] || npm install --silent
  echo "→ demo folder"
  ./setup.sh "$OUT/space"
  ldflags="-X main.version=$(git -C "$repo" describe --tags --abbrev=0 2>/dev/null || echo dev)"
  (cd "$repo" && CGO_ENABLED=0 GOOS=linux go build -ldflags "$ldflags" -o "$OUT/bin/kropka" ./cmd/kropka)
  # The QR code kropka will print, for the phone's viewfinder.
  (cd "$repo" && go run ./docs/demo/_qr "$KROPKA_URL" "$OUT/qr.png")

  # The network. Its subnet must not be one this machine is on, or the phone
  # can't tell them apart: set DEMO_SUBNET and DEMO_IP if yours is.
  if ! docker network inspect "$NET" >/dev/null 2>&1; then
    if ip route | grep -q "^${SUBNET%.*/*}\."; then
      echo "this machine is on $SUBNET already: set DEMO_SUBNET and DEMO_IP" >&2
      exit 1
    fi
    docker network create --subnet "$SUBNET" "$NET" >/dev/null
  fi
  docker rm -f kropka-demo >/dev/null 2>&1 || true
  run=(--name kropka-demo --network "$NET" --ip "$IP" --user "$(id -u):$(id -g)"
    -e HOME=/home/you -e KROPKA_TOKEN=demo -e KROPKA_CACHE_DIR=/cache
    -e "PATH=/opt/kropka:/usr/local/bin:/usr/bin:/bin"
    -v "$OUT/bin:/opt/kropka:ro" -v "$OUT/cache:/cache" -v "$OUT/home:/home/you"
    -v "$OUT/space:/home/you/Pictures/space:ro")

  echo "→ making thumbnails"
  docker run -d --rm "${run[@]}" --entrypoint kropka "$IMAGE" --lan --quiet /home/you/Pictures/space >/dev/null
  node phone.mjs --warm
  docker rm -f kropka-demo >/dev/null

  echo "→ recording"
  # Not --rm: VHS leaves its frames in the container's /tmp (moving them to a
  # mounted folder fails), so they're copied out once it's done.
  docker create "${run[@]}" -v "$here/demo.tape:/demo.tape:ro" "$IMAGE" /demo.tape >/dev/null
  docker start -a kropka-demo >/dev/null &
  tape=$!
  node phone.mjs
  wait "$tape"
  docker cp -q kropka-demo:/tmp/frames "$OUT/terminal"
  docker rm kropka-demo >/dev/null
  mv "$OUT/home/.t0" "$OUT/terminal.t0"
  mv "$OUT/home/.t1" "$OUT/terminal.t1"
fi

# The phone's screenshots, at a steady frame rate.
ffmpeg -hide_banner -loglevel error -y -f concat -safe 0 -i "$OUT/phone/frames.ffconcat" \
  -vf "fps=25,format=yuv420p" -c:v libx264 -crf 16 "$OUT/phone.mp4"

# The terminal: VHS's frames come in two layers, the text and the cursor.
term_fps=$(awk '$1 == "Set" && $2 == "Framerate" { print $3 }' demo.tape)
term_frames=("$OUT"/terminal/frame-text-*.png)
term=(-framerate "$term_fps" -i "$OUT/terminal/frame-text-%05d.png"
  -framerate "$term_fps" -i "$OUT/terminal/frame-cursor-%05d.png")
term_h=$(ffprobe -v error -show_entries stream=height -of csv=p=0 "${term_frames[0]}")
panel_h=$((term_h + 2 * PAD))

# VHS drops frames when the machine is busy, which shortens its video; the
# tape wrote the wall clock at its start and end, so stretch it back.
dur() { ffprobe -v error -show_entries format=duration -of csv=p=0 "$1"; }
read -r term_t0 term_t1 phone_t0 phone_done < <(echo "$(cat "$OUT/terminal.t0") $(cat "$OUT/terminal.t1") $(cat "$OUT/phone.t0") $(cat "$OUT/phone.done")")
stretch=$(awk "BEGIN { print ($term_t1 - $term_t0) / 1e6 / (${#term_frames[@]} / $term_fps) }")

# The tape stops kropka 2 s before its end (see demo.tape), so the phone
# should be done by then.
late=$(awk "BEGIN { print $phone_done / 1000 - ($term_t1 / 1e6 - 2) }")
if awk "BEGIN { exit !($late > 0) }"; then
  echo "warning: the phone finished ${late}s after Ctrl+C: make the tape's Sleep longer" >&2
fi

# Line the two up by the wall clock: both noted when their video starts.
skew=$(awk "BEGIN { print $phone_t0 / 1000 - $term_t0 / 1e6 }") # > 0: the terminal started first
echo "→ terminal is ${skew}s ahead of the phone, stretched ×$stretch"
# The phone starts filming once the page is loaded: hold its first frame (the
# camera) until then. (If it started first, drop the extra.)
if awk "BEGIN { exit !($skew > 0) }"; then
  phone_align="tpad=start_duration=$skew:start_mode=clone"
else
  phone_align="trim=start=${skew#-},setpts=PTS-STARTPTS"
fi
term_len=$(awk "BEGIN { print ($term_t1 - $term_t0) / 1e6 }")
phone_len=$(awk "BEGIN { print $(dur "$OUT/phone.mp4") + $skew }")
length=$(awk "BEGIN { print ($term_len > $phone_len ? $term_len : $phone_len) }")

# Side by side, the shorter one held on its last frame until the longer ends.
# The terminal stays at 1:1, and only the phone is scaled, to its height. It
# all stays RGB (the overlay would go to yuv420p, which smears colored text).
# One pass makes the MP4 and, for the GIF, lossless PNG frames at 12 fps.
rm -rf "$OUT/frames"
mkdir -p "$OUT/frames"
ffmpeg -hide_banner -loglevel error -y "${term[@]}" -i "$OUT/phone.mp4" -filter_complex "
  [0][1]overlay=format=rgb,setpts=PTS*$stretch,fps=25,pad=iw+2*$PAD:ih+2*$PAD:$PAD:$PAD:color=$TERM_BG,tpad=stop=-1:stop_mode=clone,format=rgb24[t];
  [2]$phone_align,fps=25,scale=-1:$panel_h:flags=lanczos,setsar=1,format=rgb24,tpad=stop=-1:stop_mode=clone,pad=iw+$GAP:ih:$GAP:0:color=$BG[p];
  [t][p]hstack,pad=iw+2*$GAP:ih+2*$GAP:$GAP:$GAP:color=$BG,split[v][g];
  [v]pad=ceil(iw/2)*2:ceil(ih/2)*2:color=$BG[mp4];
  [g]fps=12[gif]" \
  -map "[mp4]" -t "$length" -c:v libx264 -pix_fmt yuv420p -crf 18 -movflags +faststart "$OUT/demo.mp4" \
  -map "[gif]" -t "$length" "$OUT/frames/%04d.png"
echo "→ $OUT/demo.mp4"

# GIF: 12 fps, from the PNG frames.
if command -v gifski >/dev/null; then
  gifski --quiet --fps 12 --quality 70 -o "$OUT/demo.gif" "$OUT"/frames/*.png
else
  ffmpeg -hide_banner -loglevel error -y -framerate 12 -i "$OUT/frames/%04d.png" -vf \
    "split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
    "$OUT/demo.gif"
fi
rm -rf "$OUT/frames"
echo "→ $OUT/demo.gif ($(ffprobe -v error -show_entries stream=width,height -of csv=s=x:p=0 "$OUT/demo.gif"), $(du -h "$OUT/demo.gif" | cut -f1))"
