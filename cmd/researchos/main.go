// Command researchos is the ResearchOS desktop application. It opens the MyGo
// native window, whose backend draws on the GPU (Direct3D 11 on Windows), and
// renders the AppShell.
package main

import (
	"context"
	"log"
	"os"

	mg "github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/boot"
	uimygo "researchos/internal/ui/mygo"
)

func main() {
	ctx := context.Background()
	runtime, err := boot.New(ctx)
	if err != nil {
		log.Fatalf("researchos: %v", err)
	}
	defer runtime.Close()

	// The window's Content is the pure shell view: the normal MyGo window
	// paints it on the GPU. No renderer or software backend is selected here,
	// so MyGo keeps its default hardware-accelerated path.
	mg.NewWindow(mg.WindowOptions{
		Title:     "ResearchOS",
		Width:     1440,
		Height:    900,
		MinWidth:  1024,
		MinHeight: 700,
		StateKey:  "main",
		Content:   ui.View(runtime.View),
	})

	mg.App.WhenReady(func() {
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
