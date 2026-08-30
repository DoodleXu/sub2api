package main

import (
	"context"
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// The desktop build embeds the separately built Vue application. Keeping the
// web bundle under desktop/ prevents the server's Web build from accidentally
// being shipped as the native client.
//
// `all:` also includes the checked-in placeholder used by `go test ./...`
// before the first frontend build.
//
//go:embed all:frontend/dist
var frontendAssets embed.FS

func main() {
	if handled, exitCode := runToolHelper(os.Args[1:]); handled {
		os.Exit(exitCode)
	}
	app := NewApp()

	if err := wails.Run(&options.App{
		Title:            "神奇AI助手",
		Width:            1240,
		Height:           820,
		MinWidth:         960,
		MinHeight:        640,
		BackgroundColour: &options.RGBA{R: 9, G: 20, B: 31, A: 1},
		AssetServer: &assetserver.Options{
			Assets: frontendAssets,
		},
		// A second process must not race the credential/config stores. Bring the
		// existing window to the foreground instead of starting another client.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "ai.clol.site.magic-ai-assistant",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				wailsruntime.WindowShow(appContext(app))
			},
		},
		// File selection is handled by the typed Go bindings; prevent the
		// embedded WebView from navigating to an accidentally dropped file.
		DragAndDrop: &options.DragAndDrop{DisableWebViewDrop: true},
		OnStartup:   app.startup,
		Bind: []interface{}{
			app,
		},
	}); err != nil {
		log.Fatal(err)
	}
}

// appContext keeps the second-instance callback independent from App's
// unexported context field while still safely handling a launch race before
// OnStartup has fired.
func appContext(app *App) context.Context {
	return app.appContext()
}
