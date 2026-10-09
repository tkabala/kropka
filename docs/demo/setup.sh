#!/usr/bin/env bash
# Builds the folder the demo serves: NASA photos and a video (public domain,
# from images.nasa.gov), a Markdown note and a Python script. The downloads
# are kept in .assets/ and checked against the hashes below.
#
#   setup.sh <demo dir>
set -euo pipefail

dir=${1:?usage: setup.sh <demo dir>}
here=$(cd "$(dirname "$0")" && pwd)
assets=$here/.assets
mkdir -p "$assets"

base=https://images-assets.nasa.gov
# name in the folder | NASA id | file | sha256
files=(
  "aldrin-apollo-11.jpg|as11-40-5903|orig.jpg|e34e716f1dd66040e9fd89454f3a390b11ecd9619321b963b0b7631dffe32021"
  "earthrise-apollo-8.jpg|as08-14-2383|large.jpg|3263385f29f5f511a4b9cb081d9fbf73e4dbf708ca2d99c823247b675e223ae9"
  "blue-marble-apollo-17.jpg|as17-148-22727|large.jpg|36dad3314b64319ea75465c4293719eb131d943566eb462e8bac3281e02c3bd4"
  "artemis-on-the-pad.jpg|KSC-20221104-PH-ILW01_0298|large.jpg|7c16730567c14b07113c19b287370c50f01c56cf55eb015b44fadd4bde12b38a"
  "sls-in-the-vab.jpg|KSC-20220816-PH-FMX01_0067|large.jpg|172dcb0260e346cccf73e4d150651ed87132b0035b22cc0a7de89605b4b7ee04"
  "untethered-spacewalk.jpg|s84-27017|large.jpg|011931746cbddd5442b5d8914d9d8806126d4c4e000a795db15cac5634fd1968"
  "aurora-from-iss.jpg|iss030e119777|large.jpg|ef823521c147dbeae2375aefe1f6e77d92c283b8b95d9593bad42a1ba6daa070"
  "city-lights-from-iss.jpg|iss035e013107|large.jpg|91dfe76ef7fca2d06349ab6eefba8ba14a124df751b89cdb91edfd2fbf2cb5a8"
)
video_id=KSC-20220816-MH-FMX01-0001-Artemis_1_Rollout_for_Launch_TIMELAPSE-3311906
video_sha=ba236e21ee8a8f4b264f82b20d5ec90f6b96b33a0293372ad74b79e4a319cbd7

fetch() { # url file sha256
  if [ ! -f "$assets/$2" ]; then
    echo "  downloading $2"
    curl -fsSL --retry 3 -o "$assets/$2.part" "$1"
    mv "$assets/$2.part" "$assets/$2"
  fi
  echo "$3  $assets/$2" | sha256sum --check --quiet || { rm -f "$assets/$2"; exit 1; }
}

rm -rf "$dir"
mkdir -p "$dir"

for f in "${files[@]}"; do
  IFS='|' read -r name id size sha <<<"$f"
  fetch "$base/image/$id/$id~$size" "$id~$size" "$sha"
  cp "$assets/$id~$size" "$dir/$name"
done
# Cropped to the phone's shape around Aldrin, so the phone can zoom into his
# visor and still bring it to the middle (the viewer keeps the photo
# covering the screen, and in the full frame the visor is at the very top).
ffmpeg -hide_banner -loglevel error -y -i "$assets/as11-40-5903~orig.jpg" \
  -vf crop=1700:3000:1800:0 -q:v 2 "$dir/aldrin-apollo-11.jpg"

# 12 s of the rollout, from the moment the rocket shows up in the door. VP9:
# Playwright's Chromium can't play H.264.
fetch "$base/video/$video_id/$video_id~small.mp4" rollout.mp4 "$video_sha"
ffmpeg -hide_banner -loglevel error -y -ss 36 -t 12 -i "$assets/rollout.mp4" -an \
  -c:v libvpx-vp9 -b:v 0 -crf 34 -row-mt 1 "$dir/artemis-rollout.webm"

cat >"$dir/notes.md" <<'EOF'
# Space wallpapers

Picks for the new wallpapers, all from the
[NASA Image and Video Library](https://images.nasa.gov).
NASA's photos are in the public domain.

| photo | mission | year |
|---|---|---:|
| Aldrin on the Moon | Apollo 11 | 1969 |
| Earthrise | Apollo 8 | 1968 |
| Blue Marble | Apollo 17 | 1972 |
| Untethered spacewalk | STS-41B | 1984 |
| SLS on the pad | Artemis I | 2022 |

## To do

- [x] crop for the phone
- [x] grab the rollout timelapse
- [ ] find a sharper aurora
EOF

cat >"$dir/fetch.py" <<'EOF'
"""Print the originals of NASA images."""

import json
import sys
from urllib.request import urlopen

API = "https://images-api.nasa.gov/search"


def get(url):
    with urlopen(url) as r:
        return json.load(r)


def original(nasa_id):
    q = get(f"{API}?nasa_id={nasa_id}")
    item = q["collection"]["items"][0]
    for url in get(item["href"]):
        if "~orig." in url:
            return url


for nasa_id in sys.argv[1:]:
    print(nasa_id, original(nasa_id))
EOF

# The grid sorts newest first: date the files so it reads in this order.
order=(aldrin-apollo-11.jpg earthrise-apollo-8.jpg blue-marble-apollo-17.jpg
  artemis-rollout.webm artemis-on-the-pad.jpg sls-in-the-vab.jpg
  untethered-spacewalk.jpg aurora-from-iss.jpg city-lights-from-iss.jpg
  notes.md fetch.py)
now=$(date +%s)
for i in "${!order[@]}"; do
  touch -d "@$((now - 600 - i * 3600))" "$dir/${order[$i]}"
done
