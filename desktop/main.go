package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"pingvakt/core/i18n"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "PingVakt",
		Width:            1320,
		Height:           840,
		MinWidth:         760,
		MinHeight:        520,
		AssetServer:      &assetserver.Options{Assets: assets, Handler: http.HandlerFunc(app.serveI18n)},
		BackgroundColour: &options.RGBA{R: 15, G: 18, B: 22, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "se.pingvakt.desktop",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				runtime.WindowUnminimise(app.ctx)
				runtime.WindowShow(app.ctx)
			},
		},
		Windows: &windows.Options{Theme: windows.SystemDefault},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
			About:    &mac.AboutInfo{Title: "PingVakt " + Version, Message: i18n.T("Monitoring of latency and packet loss."), Icon: icon},
		},
		Linux: &linux.Options{ProgramName: "PingVakt", Icon: icon},
	})
	if err != nil {
		log.Fatal(err)
	}
}
