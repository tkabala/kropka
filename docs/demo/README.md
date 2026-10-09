# Demo recording

Records the README demo: a terminal on the left runs `kropka --lan`, and a
phone on the right scans its QR code and browses the folder: it zooms into a
photo, plays a video, opens a note and a script, and downloads it all as a
zip. Then Ctrl+C.

```sh
make demo        # or docs/demo/record.sh
```

This writes `out/demo.gif` and `out/demo.mp4`. To redo only those from the
last recordings, run `record.sh --stitch`.

## What you need

- Docker, `go`, `node`, `ffmpeg`
- [gifski](https://gif.ski), optional, for a better GIF

With [mise](https://mise.jdx.dev), `mise install` in this folder gets `node`
and gifski; then run `mise x -- ./record.sh`. The first run installs
Playwright into `node_modules`; if it asks for a browser, run
`npx playwright install chromium`.

## How it works

| File | Does |
|---|---|
| `setup.sh` | builds the folder: photos and a timelapse from the [NASA Image and Video Library](https://images.nasa.gov) (public domain; downloaded once into `.assets/` and checked against their hashes), a Markdown note and a Python script |
| `demo.tape` | the terminal ([VHS](https://github.com/charmbracelet/vhs)): `kropka --lan --quiet`, a fixed wait, Ctrl+C |
| `phone.mjs` | the phone: Playwright with an iPhone viewport, with touch input sent over CDP (taps, swipes, a pinch) and a dot drawn under each finger; a camera viewfinder with the terminal's QR code stands in for scanning it |
| `_qr/` | writes that QR code as a PNG, with the library and level kropka uses, so it's the same code |
| `record.sh` | runs the tape and the phone at the same time, then puts the two side by side |

The tape runs in VHS's Docker image, on a Docker network of its own
(`kropka-demo`, `192.168.1.0/24`), so kropka prints a home-network address,
`192.168.1.42`, and the folder is `~/Pictures/space`. The phone runs on this
machine and opens that address. If this machine is on `192.168.1.0/24`
itself, set `DEMO_SUBNET` and `DEMO_IP`.

The phone has to be done before the tape presses Ctrl+C. It prints when each
step starts, and `record.sh` warns if it finished late: then make the tape's
`Sleep` longer. Each half writes the time its recording started, and
`record.sh` uses those times to line the videos up. VHS drops frames when the
machine is busy, so the terminal is stretched back to real time using the
start and end times the tape writes.

The terminal is drawn at the size it has in the GIF, and never scaled: VHS
writes PNG frames, and `record.sh` puts them side by side with the phone
(scaled to the terminal's height) and makes the GIF from lossless frames. So
the size of the whole demo comes from `FontSize`, `Width` and `Height` in
`demo.tape` (828 x 690 px); the padding, the GIF's 12 fps and its quality
are set in `record.sh`.

To change the story, edit the steps under `story` in `phone.mjs` and the
tape's `Sleep`. `HEADED=1` shows the phone's browser while it runs.
