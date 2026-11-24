package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/app"
)

func main() {
	a := app.New()
	mygo.App.OnWindowAllClosed(func() {
		// If the user chose "Minimize to tray", the window is hidden but the
		// tray icon keeps the app alive. Do NOT quit in that case.
		if a.HasTray() {
			return
		}
		// A listener keeps the process alive with no window. Quit instead, so
		// clicking the Dock icon launches a fresh window rather than
		// activating a windowless process. main's deferred Shutdown still runs
		// after Run returns.
		mygo.App.Quit()
	})
	defer a.Shutdown()

	mygo.App.WhenReady(func() {
		// Declare the app's languages so MyGo's Locale() picks the user's
		// preferred one among them; English is the source language.
		mygo.App.SetLanguages("en", "zh-Hans")
		a.SetInitialLocale()
		// Default title bar = native OS chrome (Windows caption buttons,
		// macOS traffic lights, Linux window decorations). No Frameless,
		// no TitleBarHidden.
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "Video Downloader",
			Width:     1120,
			Height:    680,
			MinWidth:  800,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(a.View),
		})
		a.SetWindow(win)
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
