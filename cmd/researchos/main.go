// Command researchos is the ResearchOS desktop application. It opens the MyGo
// native window, whose backend draws on the GPU (Direct3D 11 on Windows), and
// renders the AppShell.
package main

import (
	"context"
	_ "embed"
	"log"
	"os"

	mg "github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/boot"
	uimygo "researchos/internal/ui/mygo"
)

// appIconPNG is the application icon (512x512 RGBA PNG, transparent
// background; the 1254px original sits next to it as appicon-source.png).
// The window backend resizes it per use: title bar, taskbar, Dock.
//
//go:embed assets/appicon.png
var appIconPNG []byte

func main() {
	ctx := context.Background()
	runtime, err := boot.New(ctx)
	if err != nil {
		log.Fatalf("researchos: %v", err)
	}
	defer runtime.Close()

	mg.App.WhenReady(func() {
		// MyGo requires windows to be created once the application is ready;
		// calling NewWindow before App.Run() reaches that point panics.
		//
		// The window's Content is the pure shell view: the normal MyGo window
		// paints it on the GPU. No renderer or software backend is selected
		// here, so MyGo keeps its default hardware-accelerated path.
		window := mg.NewWindow(mg.WindowOptions{
			Title:     "ResearchOS",
			Width:     1440,
			Height:    900,
			MinWidth:  1024,
			MinHeight: 700,
			StateKey:  "main",
			Content:   ui.View(runtime.View),
		})

		// The app icon in the title bar and taskbar button (Windows, Linux)
		// and in the Dock (macOS). A failure is cosmetic, not fatal.
		if err := window.SetIcon(appIconPNG); err != nil {
			log.Printf("researchos: window icon: %v", err)
		}
		if err := mg.App.Dock.SetIcon(appIconPNG); err != nil {
			log.Printf("researchos: dock icon: %v", err)
		}

		// SPEC-05 §5.7: the note editor's 600 ms debounce must not lose the
		// last edits when the window closes, so its flush runs synchronously
		// before the window goes away.
		window.OnClose(func(e *mg.CloseEvent) {
			runtime.Flush()
		})

		// Follow the desktop's appearance and reduced-motion setting; the
		// app applies them through the theme provider.
		uimygo.SyncSystem(runtime.Provider())
		uimygo.WatchSystem(runtime.Provider(), uimygo.InvalidateAll())
	})

	if err := mg.App.Run(); err != nil {
		log.Printf("researchos: %v", err)
		os.Exit(1)
	}
}
