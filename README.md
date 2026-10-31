# Twitch Easy Media Alerts

A Windows app that plays sounds when chat commands (`!sad`, `!angry`, …) appear in a Twitch channel's chat, shows videos and GIFs with sound in OBS, and does the same for channel point rewards. It used to be called Twitch Sound Commands.

Ready-made build: `dist\TwitchEasyMediaAlerts.exe`. It's a single exe with nothing to install.

## Features

- The channel can be given in any form: `https://www.twitch.tv/name`, a popout chat link, or just `name`. The chat is read anonymously, without logging in.
- Tabs: **Chat** (chat commands), **Channel Points** (rewards) and **OBS** (shared settings for showing videos and GIFs). Chat and Channel Points each have their own **Sounds**, **Videos** and **GIF + Sound** sub-tabs.
- Sound commands: each has its own sound (mp3, wav, ogg, flac), volume from 0 to 200 %, output device, cooldown in seconds, a "Multi-play" checkbox (see below), an on/off switch and a ▶ test button.
- Video commands: each has its own video (mp4, webm, mov, mkv), volume, scale from 5 to 100 %, and a position in the frame (drag it on the preview or use the 3×3 buttons) or "Random position", plus a cooldown.
- GIF + Sound: an animated picture (gif, webp, apng) with an optional sound (mp3, wav, ogg, flac). The picture stays up while the sound plays, or for a set number of seconds; with no sound and no time set it shows for 5 seconds. Scale, position, "Random position" and chroma key work as for videos. The GIF's own transparency is kept. GIFs have the "Multi-play" checkbox too.
- Transparent color (chroma key) for videos and GIFs: a chosen background color, such as a meme's green screen, becomes transparent. Pick it with the palette, a hex code or the eyedropper right on the preview. Similarity, Smoothness and Key Color Spill Reduction work like OBS's Chroma Key filter, with the same defaults of 400 / 80 / 100. The preview shows the result on a checkerboard.
- Videos and GIFs are shown in OBS through one shared Browser source (transparent background, sound goes to OBS), or through capture of a separate window with a background for a chroma key. They're fitted to the chosen resolution (1920×1080 by default): large ones shrink, small ones stretch, and the scale is relative to that size.
- One thing plays at a time: a sound, a video or a GIF. Until it finishes, chat commands are skipped and their cooldowns aren't used up. The ▶ test button interrupts whatever is playing, and turns into ■ Stop while it plays.
- Multi-play (off by default, per sound or GIF): a command with it ticked starts as soon as it's written in chat, over whatever plays, so several of them layer on top of each other. It doesn't hold up other commands either. Up to 10 multi-play sounds and 10 multi-play GIFs run at once; past that the command is skipped. Cooldowns still apply, and the ▶ test button still plays it alone. The Stop button stops them all.
- **Copy commands** (next to the **+ Add** button on the Sounds, Videos and GIF + Sound sub-tabs) copies that tab's enabled chat commands for a channel panel, say, a command per line. Sounds and GIFs come in a `Multi:` section with the multi-play ones and a `Single:` section with the rest, with a blank line between them; videos, which always play one at a time, are a plain list.
- An "All commands" master switch (also in the tray menu) silences every command without touching their own switches.
- Master volume (sounds, videos and GIFs), a global cooldown between any two commands, and a Stop button.
- Minimize to tray (hidden icons): the minimize button hides the window, and optionally the close button does too. The app can start minimized and connect to the channel automatically.
- Starting the exe again doesn't open a second copy; it brings up the one that's running.
- Channel Points: after you log in with Twitch, the app loads your channel's rewards, and you can choose a sound, a video or a GIF with sound for each one (see below). Chat messages never trigger rewards.
- English or Russian interface, English by default (see [Language](#language)).
- Log: who triggered which command, cooldown skips, errors.

## How commands trigger

- The command has to be a separate word in the message, and case doesn't matter: `!sad` fires in "ok that's it !SAD", but not in "!sadness".
- If a message holds several commands, the first one in the lists fires: sounds are checked first, then videos, then GIFs.
- A command's cooldown is how many seconds it won't fire again. The global cooldown is the shortest gap between any two commands.

## Channel Points

Rewards are set up on their own **Channel Points** tab and only fire when a reward is redeemed. A chat message won't trigger them, and chat commands don't react to rewards.

After you log in, the app loads the channel's rewards by itself (**Refresh** loads them again). The table shows what each reward plays. Rewards that aren't set up yet have **+ Sound**, **+ Video** and **+ GIF + Sound** buttons: an entry with the reward's title appears in the matching list below and the file picker opens right away. **Go to** jumps to a reward's entry. The **+ Add sound / + Add video / + Add GIF** buttons on the sub-tabs list the channel's rewards to choose from, or take another title. From there everything works like ordinary sounds, videos and GIFs: volume, device, scale, position, chroma key. Titles can also be typed by hand; case doesn't matter, and the field suggests the channel's reward titles.

Rewards don't get lost: while another sound or video plays they wait in a queue (up to 50). Multi-play rewards start at once, past that queue, and wait only while 10 multi-play sounds or GIFs are already on. They have no cooldowns, since Twitch has its own for rewards. The Stop button clears the queue. If a reward asks the viewer for text, the text shows in the log. This works for your own channel (channel points exist for Twitch Affiliates and Partners).

Logging in needs the Client ID of your own Twitch application, which you create once:

1. Open https://dev.twitch.tv/console/apps/create (your Twitch account needs two-factor authentication).
2. Any name; OAuth Redirect URLs: `http://localhost`; Category: "Application Integration"; Client Type: **Public**.
3. Copy the Client ID into the app (Channel Points tab → "Twitch app Client ID"). The Client Secret isn't needed.
4. Click **Log in with Twitch**: twitch.tv/activate opens with a code; confirm the login there.

The app asks only for the `channel:read:redemptions` permission (reading rewards). It never sees your password. Tokens are stored encrypted in the settings (Windows DPAPI: only your Windows account can decrypt them), refresh themselves and are checked once an hour. **Log out** revokes the token.

## Videos and GIFs in OBS

Videos and GIFs go through the same source, so you don't need a second one. Set the resolution on the OBS tab to match your OBS canvas (1920×1080 by default).

### Browser source (recommended)

A transparent background without a chroma key, the video's sound goes straight into OBS, and no extra window is needed.

1. Click **Copy** next to the address on the OBS tab. The address looks like `http://127.0.0.1:27463/<key>/obs` and doesn't change, so you only paste it once.
2. In OBS: Sources → "+" → "Browser", untick "Local file", and paste the address into "URL".
3. Set Width and Height to the resolution chosen in the app.
4. Tick "Control audio via OBS": the video's sound then gets its own line in the OBS audio mixer.

The app has to be running; the source connects to it by itself, and the OBS tab shows whether it's connected. While OBS is connected, videos play there and not in the window, so the sound isn't doubled. If port 27463 is taken by another program, the app uses a random port and writes the new address to the log (until a restart).

### Window Capture (fallback)

1. Click **Open window**. The window can't be minimized, because OBS doesn't capture minimized windows, but it can be covered by other windows or moved to a second monitor. Its close button only hides it.
2. In OBS, add a "Window Capture" source, pick the window "Twitch Easy Media Alerts — video for OBS", Capture Method "Windows 10 (1903 and up)".
3. Stretch the source over the whole canvas (right-click → Transform → Stretch to screen).
4. Source filters → "Chroma Key" with the window's background color.
5. The video's sound goes to the system output device. For a separate channel in OBS, add "Application Audio Capture" → TwitchEasyMediaAlerts.exe; otherwise "Desktop Audio" picks it up.

The window opens on startup if it was open when the app quit, and it remembers its place on the screen.

Window Capture and Game Capture can't give a transparent background: the built-in browser is drawn by a separate Edge process, and OBS sees it as opaque. That's why transparency needs the Browser source.

After the rename, a Window Capture source set up for the old Twitch Sound Commands window has to have its window picked again. The Browser source address stays the same.

## Language

The interface is in English by default. Switch it with the selector in the top right corner (English / Русский); the page reloads in the new language right away, and the choice is saved in the settings. The language also applies to log messages, the tray menu and dialogs. Messages already in the log stay in the language they were written in.

## Settings

By default they live in Documents: `Documents\TwitchEasyMediaAlerts\settings.json` (or wherever Documents has been moved, such as OneDrive). That way the settings don't depend on where the exe is, they're easy to find and back up, and they survive app updates.

The folder can be changed: App Window card → **Change folder…** (the same card has **Open folder** and the current path). The settings move to the chosen folder and the old file is deleted. If the new folder already has a `settings.json`, the app asks whether to replace it, and the old one is kept as `settings.json.bak`. The choice is remembered in `%APPDATA%\TwitchEasyMediaAlerts\settings-dir.txt`. If the chosen folder is unavailable (an unplugged USB drive, say), the app uses the settings in Documents and says so in the log.

On first start, the app moves settings from where earlier versions kept them, unless the new place already has settings: `Documents\TwitchSoundCommands` (the old name), `%APPDATA%\TwitchSoundCommands`, or next to the exe. A folder chosen in the old version is carried over too. The move is noted in the log.

The built-in browser's own data lives separately, in `%APPDATA%\TwitchEasyMediaAlerts\WebView2`. The old `%APPDATA%\TwitchSoundCommands` folder is no longer used and can be deleted.

The `TWITCH_EASY_MEDIA_ALERTS_DIR` environment variable fixes the settings folder, for example for a portable copy on a USB drive. It takes priority over the folder chosen in the app, nothing is moved into it from elsewhere, and **Change folder…** is disabled.

## Requirements

Windows 10/11 with the Microsoft Edge WebView2 Runtime (Windows 11 already has it).

The exe isn't signed, so on first start SmartScreen may show "Windows protected your PC". Click "More info" → "Run anyway".

## Building

Needs Go 1.26+; CGO isn't required.

```bat
build.bat
```

or by hand:

```bat
go build -trimpath -ldflags "-H windowsgui -s -w" -o dist\TwitchEasyMediaAlerts.exe .\src
```

Tests: `go test ./src/...`

The icon, manifest and version info are already in `src\rsrc_windows_amd64.syso`. They only need regenerating after changing `winres\winres.json` or the icon (`go run ./tools/genicon` draws the PNGs):

```bat
go install github.com/tc-hib/go-winres@latest
go generate .\src
```

## Code layout

All of the app's code is in `src`, one package per concern:

| Path | What's inside |
|---|---|
| `src/main.go` | entry point: a single running copy, WebView2 flags |
| `src/app` | ties it all together: the main window and the page's bindings, chat commands and cooldowns, channel point rewards and their queue, playing one sound, video or GIF at a time |
| `src/config` | settings: types and defaults, the settings folder, loading, saving and moving the file |
| `src/twitch` | Twitch: the chat client (IRC over TLS) with automatic reconnects, matching messages to commands, channel points (device code login, Helix API, EventSub WebSocket, token encryption) |
| `src/audio` | sound output to the chosen device through WASAPI, decoding with beep |
| `src/overlay` | the local web server behind OBS: the video/GIF page, events, reports, media files |
| `src/win` | Win32: tray and minimize/close to tray, the window OBS captures, file and folder dialogs, message boxes, clipboard, COM helpers |
| `src/ui` | the pages built into the exe: `index.html` (the interface), `overlay.html` (videos and GIFs for OBS), `chroma.js` (WebGL chroma key shared by both) |
| `src/i18n` | the language of texts made in Go |
| `src/appinfo` | the app's name, version and id |
| `winres/` | icon and manifest sources for go-winres |
| `tools/genicon` | draws the icon PNGs |
