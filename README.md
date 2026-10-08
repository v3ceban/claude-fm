# claude-fm

Watch and listen to **Claude FM** (the lo-fi stream behind Claude Code's `/radio`
command) inside a terminal pane, directly or in tmux. The video is drawn as real
pixels, the audio plays through mpv, and the picture follows the pane through
splits, zooms, window switches and resizes. Press `q` to quit. That is the
whole UI.

## Requirements

| Tool     | Why                                                                     | Install (macOS)       |
| -------- | ----------------------------------------------------------------------- | --------------------- |
| terminal | kitty graphics protocol: kitty, ghostty, WezTerm, iTerm2 ≥ 3.5, Konsole |                       |
| Go 1.27+ | build                                                                   | `brew install go`     |
| yt-dlp   | resolve the live stream to HLS media URLs                               | `brew install yt-dlp` |
| ffmpeg   | decode video and audio                                                  | `brew install ffmpeg` |
| mpv      | audio output and the playback clock                                     | `brew install mpv`    |

macOS and Linux; on Linux install the three media tools with your package
manager. Graphics support is detected at start-up by sending the kitty graphics
query (wrapped in a tmux passthrough when needed) and waiting up to two seconds
for the terminal's answer, so there is no allow-list of terminal names.

tmux is optional. Inside tmux the app enables `allow-passthrough` for its own
pane and `focus-events` for the server (restored on exit if it was off).

## Install

With a Go toolchain, straight from GitHub:

```sh
go install github.com/v3ceban/claude-fm/cmd/claude-fm@latest
claude-fm
```

The binary lands in `$(go env GOBIN)` or `$(go env GOPATH)/bin`, which is
usually `~/go/bin`; make sure that is on your `PATH`.

From a checkout:

```sh
git clone https://github.com/v3ceban/claude-fm.git
cd claude-fm
make build        # or: go build -o claude-fm ./cmd/claude-fm
./claude-fm
make install      # copies the binary to ~/.local/bin (override with PREFIX=/usr/local)
```

Flags (all optional):

```
-volume 100     volume 0-130
-fps 30         max frames per second to draw
-cell-px WxH    terminal cell size in pixels, if detection is wrong
-input FILE     play a local file / direct URL instead of Claude FM
-log FILE       write a debug log
```

## How it works

```
clau.de/radio ─► yt-dlp ─► HLS video URL + HLS audio URL
                    │                        │
                    ▼                        ▼
      ffmpeg #1 (video, -copyts)      ffmpeg #2 (audio, -copyts)
      fps=30, scale, yuv420p,         48 kHz s16le, ashowinfo
      showinfo ─► pipe                ─► pipe
                    │                        │
                    ▼                        ▼
      frame queue (bounded,           mpv (rawaudio over a pipe,
      back-pressures ffmpeg #1)       10 s read-ahead, IPC socket)
                    │                        │
                    └── frame shown when ◄── time-pos = master clock
                        clock ≥ frame pts
                    │
                    ▼
   adaptive 256-colour palette → PNG → kitty graphics escape
   (wrapped in tmux passthrough, placed at the pane's screen position)
```

**Sync.** YouTube serves video and audio as separate HLS playlists that do not
start on the same segment. Both ffmpeg processes run with `-copyts` so the
streams keep their absolute timestamps, and the `showinfo`/`ashowinfo` filters
log the pts of every frame and audio chunk. The player pairs those log lines
with the raw bytes from the pipes, trims whichever stream starts earlier, feeds
the PCM to mpv, and polls mpv's `time-pos` 25 times a second. Each video frame
is shown when the smoothed mpv clock reaches its timestamp.

Audio and video are deliberately separate processes: audio runs a few seconds
ahead inside mpv's cache, so a slow terminal or a network hiccup never reaches
the speaker, while late video frames are dropped rather than shown. ffmpeg's
pacing options (`-re`, `-readrate`) are not used; on this stream they deliver
only ~0.6x real time.

**Picture.** The pane's pixel size (cell size from tmux's `client_cell_width`,
`TIOCGWINSZ` or the `CSI 16 t` query outside tmux) picks the largest of 1280×720,
854×480, 640×360 or 426×240 that fits the 16:9 area the image will cover; the
terminal scales it to that area. Each yuv420p frame is reduced to the 256 most
used colours of that frame (exact for this flat art) and encoded as a paletted
PNG in about ten milliseconds. Frames alternate between two kitty image ids and
the previous id is deleted after each frame, so the terminal never accumulates
images. Images sit below the text layer. Inside tmux the frame is addressed by
absolute screen position with the cursor saved and restored around it, so other
panes are untouched; pane visibility is checked on focus events and every 200 ms,
the picture is deleted the moment the pane is hidden and comes back when shown.

Shrinking the pane just re-fits the picture. Growing it past the current source
resolution reconnects once with a bigger source, after the resize has settled
for 300 ms so a window drag does not reconnect at every intermediate size.

## Layout

```
cmd/claude-fm/      CLI and presentation loop
internal/render/       adaptive quantizer and PNG frame encoder
internal/pipeline/     ffmpeg + mpv session, frame/audio queues, A/V clock
internal/stream/       yt-dlp resolution
internal/term/         raw mode, graphics probe, tmux integration, kitty output, quit key
```

## Development

```sh
make test-short   # unit tests
make test         # also runs the ffmpeg/mpv integration test on a generated clip
make lint         # go vet + gopls check (uses go run if gopls is not installed)
```

## Other ways to listen

Claude Code's own `/radio` opens https://clau.de/radio in a browser, or prints
the URL when there is no browser. Everything below is unofficial, including
this project.

| Project                                                                     | What you get                                                                                     | Runs on                                        | Needs                                    |
| --------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ | ---------------------------------------------- | ---------------------------------------- |
| [claude-fm](.) (this repo)                                                  | video and audio in a terminal pane, follows tmux splits and resizes                              | macOS, Linux; any terminal with kitty graphics | Go, yt-dlp, ffmpeg, mpv                  |
| [GithubAnant/claudefm](https://github.com/GithubAnant/claudefm)             | audio only, with pause/seek/volume controls and a small dashboard                                | macOS, Linux                                   | Node.js 18+, yt-dlp, mpv or ffplay       |
| [code-akram/cc-fm-mod](https://github.com/code-akram/cc-fm-mod)             | audio only, plus a live spectrum drawn under the Claude Code prompt via a plugin; works over SSH | macOS, Linux                                   | Go, ffmpeg, yt-dlp, Claude Code 2.1.287+ |
| [LorenzoZemp/ClaudeFMPlayer](https://github.com/LorenzoZemp/ClaudeFMPlayer) | audio only from the macOS menu bar, with Now Playing integration                                 | macOS 26+                                      | yt-dlp                                   |
| [sn0wjin19/Claude-FM-Player](https://github.com/sn0wjin19/Claude-FM-Player) | audio only in a small desktop window                                                             | Windows                                        |                                          |
| plain mpv                                                                   | `mpv --no-video https://clau.de/radio` for audio, drop the flag for a video window               | anywhere mpv runs                              | mpv, yt-dlp                              |

The difference that matters: the others play the audio track and leave the
video on YouTube. This project exists to show the picture too, with Clawd's
animation and the on-screen artist credit, without leaving the terminal.

## Notes

- Terminal emulators embedded in other programs (Neovim's `:terminal`, VS Code's
  integrated terminal, Emacs vterm) do not pass graphics through to the real
  terminal, so the start-up check refuses to run there.
- The stream URL changes when Anthropic restarts the broadcast and media URLs
  expire after a few hours; the player re-resolves and reconnects on its own.
- Cost on an M-series Mac at 1280×720: roughly 50% of one core for the player,
  10% for ffmpeg, about 270 KB per frame of terminal output at 30 fps.
