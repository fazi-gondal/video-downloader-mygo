package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/app"
)

func main() {
	a := app.New()
	mygo.App.OnWindowAllClosed(a.Shutdown)
	defer a.Shutdown()

	mygo.App.WhenReady(func() {
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
