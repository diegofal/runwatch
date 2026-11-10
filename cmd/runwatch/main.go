// Command runwatch watches the GitHub Actions self-hosted runners on this
// PC: a tray icon with a flyout, and a dashboard served on 127.0.0.1.
//
//	runwatch              tray app (the normal way)
//	runwatch --headless   no tray: only the dashboard server, until Ctrl+C
//	runwatch --demo       made-up runners and a pretend GitHub, to try it out
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"runwatch/internal/adapters/notify"
	"runwatch/internal/tray/shell"
	"runwatch/internal/wiring"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

func main() {
	headless := flag.Bool("headless", false, "run without the tray: only the dashboard server")
	demo := flag.Bool("demo", false, "run on made-up runners and a pretend GitHub (touches nothing real)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("runwatch", version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, *headless, *demo)
	stop()
	if err != nil {
		// The tray build has no console: the notification is what the user sees.
		fmt.Fprintf(os.Stderr, "runwatch: %v\n", err)
		if !*headless {
			_ = notify.New("").Show("Runwatch could not start", err.Error(), "") // already failing: stderr has the error
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, headless, demo bool) error {
	notifier := notify.New("")
	app, err := wiring.Start(ctx, wiring.Options{Version: version, Demo: demo, Notifier: notifier})
	if err != nil {
		return err
	}
	defer app.Close()
	notifier.SetBase(app.URL)
	fmt.Println("dashboard:", app.URL)
	if headless {
		<-ctx.Done()
		return nil
	}
	return shell.Run(ctx, shell.Options{
		Watcher: app.Watcher, URL: app.URL, LogPath: app.LogPath, Logf: app.Logf, Version: version, Demo: demo,
		SetOpener: app.SetOpener,
	})
}
