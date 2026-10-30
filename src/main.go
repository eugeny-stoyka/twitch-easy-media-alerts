// Twitch Easy Media Alerts plays sounds, videos and GIFs when chat commands
// such as !sad or channel point rewards come up on a Twitch channel.
package main

//go:generate go-winres make --in ../winres/winres.json --out rsrc --arch amd64

import (
	"os"
	"runtime"
	"strings"

	"twitch_easy_media_alerts/src/app"
	"twitch_easy_media_alerts/src/win"
)

// browserArgs let the OBS window play videos with sound without a click and
// keep rendering while other windows cover it (OBS captures it all the time).
const browserArgs = "--autoplay-policy=no-user-gesture-required" +
	" --disable-features=CalculateNativeWinOcclusion" +
	" --disable-background-timer-throttling" +
	" --disable-backgrounding-occluded-windows" +
	" --disable-renderer-backgrounding"

func main() {
	// The windows and WebView2 live on the main OS thread.
	runtime.LockOSThread()
	if !win.SingleInstance() {
		return
	}
	const env = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
	os.Setenv(env, strings.TrimSpace(os.Getenv(env)+" "+browserArgs))
	app.New().Run()
}
