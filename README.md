# Classic iPod Manager

A native Windows app for managing the music and videos on an iPod without iTunes: every iPod that shows up as a drive, from the 1st generation to the iPod classic, mini, nano and shuffle. See [Supported iPods](#supported-ipods).

## Supported iPods

| iPod | Music and playlists | Album art | Video |
|---|---|---|---|
| iPod 1st–4th generation (2001–2004), iPod mini | Yes | – (no colour screen) | – |
| iPod photo / colour display | Yes | Yes | – |
| iPod 5th generation (Video), including the late-2006 "5.5G" and U2 | Yes | Yes | Yes |
| iPod classic (6th generation 2007 and 2008, 7th generation 2009) | Yes, signed with the iPod's FireWire ID | Yes | Yes |
| iPod nano 1st and 2nd generation | Yes | Yes | – |
| iPod nano 3rd and 4th generation | Yes, signed with the iPod's FireWire ID | Yes | Yes |
| iPod shuffle 1st–4th generation | Yes, with the shuffle's own song list (iTunesSD) | – | – |
| iPod nano 5th, 6th and 7th generation | View and copy songs off | – | – |
| iPod touch, iPhone, iPad | Not possible: they don't appear as a drive | | |

- **Which iPod**: Classic iPod Manager reads the model number from `iPod_Control\Device\SysInfo` (or the serial number, or `SysInfoExtended`) and knows every model number Apple used for these iPods, so it names the iPod, uses the right album-art sizes, offers videos only where they play, and creates the right number of music folders when setting up an empty iPod. For a model it doesn't know, it goes by what the iPod's own database and artwork files show.
- **Signed libraries**: the iPod classic and the 3rd and 4th generation nano only accept a music library signed with their FireWire ID (the "hash58" signature). Classic iPod Manager reads the ID from SysInfo or SysInfoExtended, or from the iPod's USB serial number, which is the same ID, and signs the library every time it saves. If it can't find the ID the iPod opens read-only, with an explanation.
- **Newer nanos**: the 5th, 6th and 7th generation nano keep a compressed library with a signature only iTunes or Apple Music can make. Classic iPod Manager shows them and copies songs off them, but can't change them.
- **Shuffles** have no screen and play from a separate song list, `iTunesSD`, in one of two formats (1st/2nd and 3rd/4th generation). Classic iPod Manager writes it alongside the library, and adds the play counts the shuffle recorded in `iTunesStats`.
- **Formats**: the 1st and 2nd generation iPod and shuffle don't play Apple Lossless, and the 1st generation shuffle doesn't play AIFF; such songs are refused with a note to convert them in the Format Manager.
- Mac-formatted (HFS+) iPods can't be read by Windows; restore them for Windows in iTunes or the Apple Devices app first.

## Features

- **Drag and drop**: drag songs, videos or folders from Explorer onto the window to copy them to the iPod. Songs the iPod can't play (FLAC, Ogg, WMA, non-audio files) are refused with an explanation; if a drop mixes good and bad files, you can copy the playable ones. Videos the iPod can't play are converted first (see Videos). Drag songs from the list onto an Explorer window or the desktop to copy them to the computer, named "Artist - Title".
- **Add songs** one at a time, by multi-selecting files, or a whole folder (sub-folders included). MP3, AAC (.m4a/.m4b), Apple Lossless, WAV and AIFF are supported. Songs already on the iPod are skipped, and formats the iPod can't play (FLAC, Ogg, WMA) are reported instead of copied.
- **Remove songs**, singly or in bulk (Delete key or right-click). The files are deleted from the iPod and the songs disappear from every playlist.
- **Edit metadata** with a Get Info sheet: name, artist, album artist, album, grouping, composer, comments, genre, year, track/disc numbers, BPM, rating and compilation. Select several songs to edit them together. Only the fields you tick change. By default the changes are also written into the song files' own tags (ID3v2 for MP3, iTunes atoms for M4A), with artwork and other tags preserved.
- **Album artwork** in Get Info: the picture embedded in the song file, or the cover the iPod stores in its own artwork database (from iTunes). Click it to view it larger. Songs you add get their embedded cover put into the iPod's artwork database too, so the iPod shows it.
- **Playlists**: make, rename, delete and edit playlists. See below.
- **Playback**: double-click a song (or video) to play it in the app. Play/pause, previous, next and volume sit left of the display, which shows the song and a position bar you can click or drag to seek. Playback carries on down the list. See below.
- **Transfer speed**: while songs or videos are copied to or from the iPod, the display shows the speed and the time left, for example "Copying 3 of 12 · 18.4 MB/s · 1:20 left".
- **Videos**: add and remove videos in the **Videos** section under the iPod, which has **Movies**, **TV Shows** and **Music Videos** inside it (Add Videos, Add Folder, drag and drop, Delete). See below.
- **Artwork pane**: the cover of the song or video you click shows at the bottom of the left pane. Click it to view it larger, or click its heading to fold it away.
- **Format Manager** (Formats button, Ctrl+M, or right-click songs on the iPod): change the details and album artwork of songs on the computer or the iPod, and convert them between MP3, AAC, Apple Lossless, WAV, AIFF, FLAC and Ogg Vorbis at the bit rate you choose. See below.
- A song list with name, time, artist, album, genre, year, file type, bit rate, size, date added, rating and plays. Every column (Name included) can be resized freely; double-click a column edge to fit it to its contents, and right-click the header to show or hide columns. Columns sort by clicking their heading.
- Inline star ratings, search, and a Genre / Artist / Album browser.
- The row under the mouse is highlighted, in the song list and the Format Manager.
- Windows shows the program as **Classic iPod Manager** with its own icon in the taskbar, Alt+Tab, the title bar and Task Manager, and a pinned taskbar button keeps that name and icon.
- The bottom bar shows free space against the iPod's total capacity (for example "21.4 GB free of 74.4 GB") on the left, with a bar split into audio, other data and free space, and the number of songs, total time and size of the list on the right.
- Copy songs back to the computer (organised as Artist\Album\NN Title), or drag them out of the window.
- Device summary with a capacity bar, a rename option for the iPod, and safe Eject.
- **Battery**: the battery icon at the top shows the iPod's charge. Hover over it (or Tab to it) to see the percentage and whether it is charging. Classic iPod Manager asks the iPod over its USB accessory connection, the same one car stereos use, and refreshes every minute. Click it for a Battery window with a step-by-step report you can copy, which shows where the request fails if your iPod doesn't answer.
- Keeps play counts, ratings, smart playlists and On-The-Go playlists made on the iPod.
- **Options** (gear button, or Ctrl+,), in four views chosen from the menu on the left: **Interface** (theme, interface size from 75% to 200%, and text size from Small to Largest, which changes only the text and list rows), **Player** (whether the player controls sit in the top bar or the bottom bar), **Editing** (whether edits also update the song files) and **Encoders** (see Format Manager). Themes come in two styles: the classic brushed-aluminium Silver and Black (plus Auto, which matches the connected iPod's colour), and modern flat themes in Dark Blue, Space Grey, White and Red. Settings are kept in `%LOCALAPPDATA%\Classic iPod Manager\settings.json`.

## Videos

Open **Videos** under the iPod in the sidebar. Inside it are **Movies**, **TV Shows** and **Music Videos**, matching the iPod's own Videos menu. **Add Videos** picks video files, **Add Folder** adds every video in a folder, and you can drag videos onto the window. Videos added while one of the three sections is open are filed in that section on the iPod. Added from **Videos** itself, a video goes where its own iTunes tag says (TV show or music video), otherwise to Movies. To move videos to another section, drag them onto it in the sidebar or right-click → **Move to**. Select videos and press Delete (or right-click) to remove them.

Videos are offered on the iPods that play them: the 5th generation, the classic and the 3rd–5th generation nano. They play MPEG-4 videos: H.264 Baseline up to 640×480 or MPEG-4 up to 480×480, with AAC sound. Videos that already fit are copied as they are. Anything else (HD video, H.264 High profile, MKV, AVI, WMV, QuickTime .mov, MP3 sound and so on) is converted with FFmpeg first, to 640×480 H.264 with AAC sound, and you're asked before it starts. This needs `ffmpeg.exe` in one of the encoder folders (the Decoder folder is the natural place). Converted copies are kept in the main folder's `Converted Videos` folder. The original iPod 5G (not the late-2006 5.5G) needs firmware 1.2 or later for 640×480 video.

Each video gets a thumbnail for the iPod's menus: its own cover picture if it has one, otherwise a frame from about a tenth of the way in (with FFmpeg). In Get Info, **Video kind** also changes the section. TV shows are grouped by their Album name.

## Playlists

The **Playlists** heading in the sidebar lists the iPod's playlists.

- **New playlist**: click **+** next to the heading (or Ctrl+N), type a name and press Enter. Or select songs, right-click and choose **Add to Playlist → New Playlist from Selection**, or drag the songs onto the Playlists heading.
- **Add songs**: open the playlist and click **Add Songs** (Ctrl+O). A list of everything on the iPod opens: click songs to tick them (Shift+click ticks a run, the box in the heading ticks every song shown), and type in the search box to narrow the list by name, artist, album, genre or composer. Ticks are kept while you search, so you can search for one thing, tick it, then search for another. Pressing Enter in the search ticks the only match; Ctrl+Enter or **Add to Playlist** adds what's ticked. Songs already in the playlist are marked and can't be ticked again. Videos are under the **Videos** tab. Click a heading to sort. You can also drag songs onto a playlist in the sidebar, or right-click and choose **Add to Playlist**.
- In a playlist, **Add Folder** is hidden and **Remove** (or Delete) takes the selected songs out of the playlist; they stay on the iPod.
- **Reorder**: in a playlist sorted by the first (#) column, drag songs up or down. Click the # heading to get back to the playlist's own order after sorting by another column.
- **Remove songs from a playlist**: select them and press Delete, or right-click → **Remove from Playlist**. The songs stay on the iPod. (Shift+Delete, or **Remove from iPod…**, deletes them from the iPod.)
- **Rename** a playlist by double-clicking it, and **delete** it by right-clicking it. Deleting a playlist leaves its songs on the iPod.

Smart playlists, On-The-Go playlists and Podcasts come from the iPod or iTunes and are shown but can't be edited here.

## Playback

Double-click a song to play it, or select songs and press Space. The songs after it in the list play next. The controls are left of the display: previous (restarts the song if it's more than three seconds in), play/pause and next, then volume and mute. **Options → Player** moves them to the middle of the bottom bar instead.

Hovering over Classic iPod Manager's button on the Windows taskbar shows Previous, Play/Pause and Next under the window's preview, as media players do, and the preview's tooltip names the playing song. The buttons follow Windows' light or dark taskbar. Keyboard: Space play/pause, Ctrl+Left / Ctrl+Right previous / next, Ctrl+Up / Ctrl+Down volume. The mouse wheel over the controls also changes the volume.

The display shows the playing song, its artist and album, and a position bar: click or drag it to jump. The playing song has a speaker beside its name.

Videos play in a small window you can move by its title bar; ⤢ makes it full screen and ✕ stops it. Songs in the Format Manager play the same way (double-click), including songs on the computer.

MP3, AAC, WAV, FLAC and Ogg play directly. AIFF, Apple Lossless and other formats are decoded first with the encoders in the main folder (FFmpeg in the Decoder folder covers everything); the five most recent are kept in `Temp\Playback` and cleared when the app starts. Playback reads the files straight from the iPod through a small web server on 127.0.0.1 (a random port, with a random key) that only Classic iPod Manager uses.

## Format Manager

Add songs from the computer (Add Files, Add Folder, or drag them onto the window: any format, including FLAC, Ogg, Opus and WMA), or right-click songs on the iPod and choose **Edit or Convert in Format Manager**.

- **Details and artwork**: select one or more songs and edit their details on the right. Choose a picture, or drop one onto the artwork, to give them a new cover, or remove it. **Save Changes** writes the changes into the files (MP3, AAC/Apple Lossless, FLAC and Ogg Vorbis/Opus can hold them; WAV and AIFF can't). For iPod songs the iPod's library and its own cover are updated as well.
- **Converting**: pick a format and bit rate under **Convert to**, then click **Convert**. Several songs are converted at once.
  - Songs from the iPod are first copied to `iPod Originals\Artist\Album`, then converted and replaced on the iPod, keeping their play counts, ratings and playlists. FLAC and Ogg can't play on an iPod, so iPod songs converted to those are only copied.
  - Songs from the computer stay as they are; tick **Add converted computer songs to the iPod** to put the new versions on the iPod.
  - A copy of every converted song goes to `Converted Music\Artist\Album`.
  - **Details: Choose…** under Convert to picks which details are copied into the converted files: name, artist, album artist, album, genre, year, track and disc numbers, composer, grouping, comments, BPM, compilation and album artwork. Unticked ones are left out of the new files (the copies are still filed by artist and album). Songs on the iPod keep their details in the iPod's own library so they still list properly. The choice is remembered.
  - Songs going to the iPod are kept within what it can play (16-bit, up to 48 kHz, stereo).

### Main folder and encoders

The main folder is the folder `Classic iPod Manager.exe` is in. If that can't be written to (for example inside Program Files), `%LOCALAPPDATA%\Classic iPod Manager` is used. It holds:

| Folder | What goes there |
|---|---|
| `Encoders\MP3` | `lame.exe`, or `ffmpeg.exe` |
| `Encoders\AAC` | `qaac64.exe` (needs Apple's AAC components, which come with iTunes or the Apple Devices app), `fdkaac.exe`, `neroAacEnc.exe`, or `ffmpeg.exe` |
| `Encoders\Apple Lossless` | `refalac64.exe`, `qaac64.exe`, or `ffmpeg.exe` |
| `Encoders\FLAC` | `flac.exe`, or `ffmpeg.exe` |
| `Encoders\Ogg Vorbis` | `oggenc2.exe`, or `ffmpeg.exe` |
| `Encoders\Decoder` | `ffmpeg.exe`: reads formats Classic iPod Manager can't read itself (FLAC, Ogg, Opus, WMA, …) and changes sample rates |
| `Converted Music` | a copy of every converted song |
| `Converted Videos` | videos converted for the iPod |
| `iPod Originals` | iPod songs as they were before being converted |

Put an encoder into its folder (or a folder inside it, as it comes out of its zip file) and Classic iPod Manager finds it. WAV and AIFF need no encoder. A program that writes several formats is also used for any format whose own folder is empty: one `ffmpeg.exe` anywhere in the encoder folders covers MP3, AAC, Apple Lossless, FLAC and Ogg Vorbis, and `qaac64.exe` in the AAC folder also covers Apple Lossless. A program in a format's own folder always comes first. Without FFmpeg, Classic iPod Manager can still decode MP3 with LAME, FLAC with flac.exe, Apple Lossless with refalac or qaac, AAC with qaac, and Ogg Vorbis with oggdec.exe placed in the Ogg Vorbis folder.

**Options → Encoders** shows which program each format will use, lets you choose a different folder for each (inside the main folder, or any full path), and picks between programs when a folder has more than one.

## Requirements

- Windows 10 or 11 (64-bit). The Microsoft Edge WebView2 Runtime ships with both.
- An iPod formatted for Windows (FAT32). A Mac-formatted iPod has to be restored for Windows first, in iTunes or the Apple Devices app.
- Any iPod that appears as a drive (see [Supported iPods](#supported-ipods)).

## Using it

1. Run `Classic iPod Manager.exe` (no installer needed) and plug in the iPod. It appears under **Devices**.
2. If the iPod has never been used with iTunes, Classic iPod Manager offers to create an empty music library.
3. Use **Add Songs**, **Add Folder**, **Get Info** (Ctrl+I or Enter) and **Remove** (Delete). Double-click a song to play it.
4. Click **Eject** (⏏) before unplugging so the iPod can rebuild its menus.

Every change is written to the iPod straight away. The first time Classic iPod Manager changes an iPod during a session, it backs up the database to `iPod_Control\iTunes\iTunesDB.podmanager.bak` (the file keeps its old name) and to `%APPDATA%\Classic iPod Manager\Backups\<iPod name>\`. The last 10 copies are kept there. To restore a backup, copy one back over `iPod_Control\iTunes\iTunesDB`.

Keyboard: Ctrl+O add songs · Ctrl+I get info · Ctrl+N new playlist · Space play/pause · Ctrl+Left/Right previous/next · Ctrl+Up/Down volume · Ctrl+M Format Manager · Ctrl+F search · Ctrl+B browser · Ctrl+A select all · Delete remove · Enter get info · Ctrl+, options · Ctrl+plus / Ctrl+minus / Ctrl+0 (or Ctrl+mouse wheel) interface size.

## Building from source

Requires Go 1.22 or newer. From a Windows command prompt:

```
build.bat
```

or from any OS:

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o "Classic iPod Manager.exe" .
```

The icon, manifest and version info are in `rsrc_windows_amd64.syso`, which is generated from `winres/` with [go-winres](https://github.com/tc-hib/go-winres) (`go-winres make --in winres/winres.json --arch amd64`).

On Linux or macOS, `go run . -roots /path/to/mounted/ipod -pick /path/to/music` starts a small development server at http://127.0.0.1:8765 with the same interface. Add `-battery 72,charging` to simulate a battery reading.

`cmd/itdbtool` is a diagnostics helper: `itdbtool dump E:\iPod_Control\iTunes\iTunesDB` lists what's in a database.

### Layout

| Path | What it does |
|---|---|
| `internal/itdb` | Reads and writes the iTunesDB, including library sort indexes, album and artist lists, Play Counts and On-The-Go playlists |
| `internal/media` | Reads audio files of any common format (length, bit rate, tags, artwork) and writes tags and artwork (ID3v2, MP4, FLAC, Ogg Vorbis/Opus) |
| `internal/convert` | Finds encoders in their folders, runs decode → encode → tag conversions, and converts videos for the iPod with FFmpeg |
| `internal/iap` | Just enough of the iPod Accessory Protocol to ask for the battery level over USB |
| `internal/ipod` | iPod layout, model detection for every generation (`model.go`, `models_gen.go`), safe saving with backups, the hash58 signature (`hash58.go`), the shuffle's iTunesSD (`shuffle.go`), file naming, writing the iPod's artwork database |
| `internal/app` | Operations the UI calls, background jobs with progress and transfer speed, playlists, and the playback server |
| `ui/index.html` | The interface (embedded in the exe) |
| `*_windows.go` | Window, native file dialogs, drive detection, eject, interface zoom, dragging songs out to Explorer, the taskbar identity and preview media buttons (glyphs drawn in `glyphs.go`), the iPod's USB accessory (HID) interface for the battery |
| `third_party/go-webview2` | [go-webview2](https://github.com/jchv/go-webview2) (MIT) with a small patch that passes the paths of files dropped on the window |

## Limitations

- The 5th–7th generation nano can only be viewed and copied from (their libraries need iTunes' own signatures). Photos and the shuffle's spoken VoiceOver menus aren't managed.
- Podcasts and audiobooks aren't managed separately. .m4b files are added as audiobooks.
- Smart playlists can't be created or edited.
- The iPod classic and nano 3G/4G signature and the shuffle song lists follow libgpod's documented formats (the signature is checked against libgpod's own output in the tests); they haven't been tried on every model.
- Playback in the app uses the WebView's own players: MPEG-4 Part 2 videos (rather than H.264) may not play in the app even though they play on the iPod.
- Classic iPod Manager doesn't include encoders; put the ones you want to use in the encoder folders (see Format Manager).
- The battery reading depends on the iPod answering over its USB accessory interface. If it doesn't, the battery icon still shows full and green while the iPod is connected, and the popover says the level is unavailable. Some firmware reports no level while charging; then only "Charging" is shown.

The model list, the iPod classic signature (hash58) and the shuffle formats come from [libgpod](https://sourceforge.net/projects/gtkpod/) (the hash58 code is under libgpod's BSD-style licence, reproduced in `internal/ipod/hash58.go`).

Classic iPod Manager was called PodManager up to version 1.5. Settings and backups from the old name are moved over automatically the first time it runs.
