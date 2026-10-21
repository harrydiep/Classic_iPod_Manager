# PodManager

A native Windows app for managing the music on an iPod 5th generation (iPod Video, 30/60/80 GB, including the late‑2006 "5.5G" and U2 editions) without iTunes.

## Features

- **Drag and drop**: drag songs or folders from Explorer onto the window to copy them to the iPod. Files the iPod can't play (FLAC, Ogg, WMA, non-audio files) are refused with an explanation; if a drop mixes good and bad files, you can copy the playable ones. Drag songs from the list onto an Explorer window or the desktop to copy them to the computer, named "Artist - Title".
- **Add songs** one at a time, by multi-selecting files, or a whole folder (sub-folders included). MP3, AAC (.m4a/.m4b), Apple Lossless, WAV and AIFF are supported. Songs already on the iPod are skipped, and formats the iPod can't play (FLAC, Ogg, WMA) are reported instead of copied.
- **Remove songs**, singly or in bulk (Delete key or right-click). The files are deleted from the iPod and the songs disappear from every playlist.
- **Edit metadata** with a Get Info sheet: name, artist, album artist, album, grouping, composer, comments, genre, year, track/disc numbers, BPM, rating and compilation. Select several songs to edit them together. Only the fields you tick change. By default the changes are also written into the song files' own tags (ID3v2 for MP3, iTunes atoms for M4A), with artwork and other tags preserved.
- **Album artwork** in Get Info: the picture embedded in the song file, or the cover the iPod stores in its own artwork database (from iTunes). Click it to view it larger.
- A song list with name, time, artist, album, genre, year, file type, bit rate, size, date added, rating and plays. Every column (Name included) can be resized freely; double-click a column edge to fit it to its contents, and right-click the header to show or hide columns. Columns sort by clicking their heading.
- Inline star ratings, search, and a Genre / Artist / Album browser.
- The bottom bar shows free space against the iPod's total capacity (for example "21.4 GB free of 74.4 GB"), with a bar split into audio, other data and free space.
- Copy songs back to the computer (organised as Artist\Album\NN Title).
- Device summary with a capacity bar, a rename option for the iPod, and safe Eject.
- **Battery**: the battery icon at the top shows the iPod's charge. Hover over it (or Tab to it) to see the percentage and whether it is charging. PodManager asks the iPod over its USB accessory connection, the same one car stereos use, and refreshes every minute. Click it for a Battery window with a step-by-step report you can copy, which shows where the request fails if your iPod doesn't answer.
- Keeps play counts, ratings, smart playlists and On-The-Go playlists made on the iPod.
- **Options** (gear button, or Ctrl+,): interface size from 75% to 200%, text size (Small to Largest, which changes only the text and list rows), theme, and whether edits also update the song files. Themes come in two styles: the classic brushed-aluminium Silver and Black (plus Auto, which matches the connected iPod's colour), and modern flat themes in Dark Blue, Space Grey, White and Red. Settings are kept in `%LOCALAPPDATA%\PodManager\settings.json`.

## Requirements

- Windows 10 or 11 (64-bit). The Microsoft Edge WebView2 Runtime ships with both.
- An iPod formatted for Windows (FAT32). A Mac-formatted iPod has to be restored for Windows first, in iTunes or the Apple Devices app.
- iPod classic 6th/7th generation and newer iPods sign their database with a checksum. PodManager detects these and opens them read-only. You can still browse them and copy songs off them.

## Using it

1. Run `PodManager.exe` (no installer needed) and plug in the iPod. It appears under **Devices**.
2. If the iPod has never been used with iTunes, PodManager offers to create an empty music library.
3. Use **Add Songs**, **Add Folder**, **Get Info** (Ctrl+I or double-click) and **Remove** (Delete).
4. Click **Eject** (⏏) before unplugging so the iPod can rebuild its menus.

Every change is written to the iPod straight away. The first time PodManager changes an iPod during a session, it backs up the database to `iPod_Control\iTunes\iTunesDB.podmanager.bak` and to `%APPDATA%\PodManager\Backups\<iPod name>\`. The last 10 copies are kept there. To restore a backup, copy one back over `iPod_Control\iTunes\iTunesDB`.

Keyboard: Ctrl+O add songs · Ctrl+I get info · Ctrl+F search · Ctrl+B browser · Ctrl+A select all · Delete remove · Enter get info · Ctrl+, options · Ctrl+plus / Ctrl+minus / Ctrl+0 (or Ctrl+mouse wheel) interface size.

## Building from source

Requires Go 1.22 or newer. From a Windows command prompt:

```
build.bat
```

or from any OS:

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o PodManager.exe .
```

The icon, manifest and version info are in `rsrc_windows_amd64.syso`, which is generated from `winres/` with [go-winres](https://github.com/tc-hib/go-winres) (`go-winres make --in winres/winres.json --arch amd64`).

On Linux or macOS, `go run . -roots /path/to/mounted/ipod -pick /path/to/music` starts a small development server at http://127.0.0.1:8765 with the same interface. Add `-battery 72,charging` to simulate a battery reading.

`cmd/itdbtool` is a diagnostics helper: `itdbtool dump E:\iPod_Control\iTunes\iTunesDB` lists what's in a database.

### Layout

| Path | What it does |
|---|---|
| `internal/itdb` | Reads and writes the iTunesDB, including library sort indexes, Play Counts and On-The-Go playlists |
| `internal/media` | Probes audio files (length, bitrate, tags) and writes ID3v2 / MP4 tags |
| `internal/iap` | Just enough of the iPod Accessory Protocol to ask for the battery level over USB |
| `internal/ipod` | iPod layout, model detection, safe saving with backups, file naming |
| `internal/app` | Operations the UI calls, plus background jobs with progress |
| `ui/index.html` | The interface (embedded in the exe) |
| `*_windows.go` | Window, native file dialogs, drive detection, eject, interface zoom, dragging songs out to Explorer, the iPod's USB accessory (HID) interface for the battery |
| `third_party/go-webview2` | [go-webview2](https://github.com/jchv/go-webview2) (MIT) with a small patch that passes the paths of files dropped on the window |

## Limitations

- Artwork is shown but can't be changed yet, and the iPod's own cover database isn't updated for new songs (the iPod shows covers from its database, not from the files). Existing artwork on the iPod is kept.
- Video, podcasts and audiobooks aren't managed separately. .m4b files are added as audiobooks.
- Playlists are shown, but creating or editing playlists isn't supported yet.
- No transcoding: convert FLAC/Ogg to Apple Lossless or AAC before adding.
- The battery reading depends on the iPod answering over its USB accessory interface. If it doesn't, the battery popover says the level is unavailable. Some firmware reports no level while charging; then only "Charging" is shown.
