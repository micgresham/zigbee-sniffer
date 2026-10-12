// Command zbsniff-desktop is a native window (Wails v2) around the zbsniff
// tethered host: it starts the unchanged cmd/zbsniff binary as a child
// process, waits for its dashboard, and shows it — instead of a terminal plus
// a browser tab. Quitting the window stops the host.
//
// The window loads the host's real http://localhost:<port> URL rather than
// serving the dashboard through Wails' AssetServer bridge: a WebView's
// WebSocket doesn't route through that bridge, and the dashboard's live feed
// is a WebSocket (same finding as wifi-sniffer's desktop shell). So the only
// thing the bridge serves is a bounce page that waits for the host and then
// navigates there with location.replace (Wails renders a 3xx body instead of
// following it).
//
// Settings (environment, since a double-clicked app has no command line):
//
//	ZBSNIFF_DATA       config/db/log directory (default: <user config dir>/zbsniff-desktop)
//	ZBSNIFF_BIN        host binary (default: bundled next to this executable)
//	ZBSNIFF_HTTP_PORT  dashboard port (default 8787, else any free port)
//	ZBSNIFF_DEMO=1     simulated network, no hardware
//	ZBSNIFF_ARGS       extra zbsniff flags, e.g. "--channel 20"
package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"time"

	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type app struct {
	ctx    context.Context
	srv    *server
	srvErr error
}

func main() {
	a := &app{}
	a.srv, a.srvErr = startServer()
	if a.srvErr != nil {
		log.Printf("zbsniff-desktop: %v", a.srvErr)
	}
	// Killed from a terminal or by the OS rather than quit from the menu:
	// Wails' shutdown hook doesn't run then, so stop the host here or it
	// would be left holding the serial port.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		if a.srv != nil {
			a.srv.stop()
		}
		os.Exit(0)
	}()

	appMenu := menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu())
	tools := appMenu.AddSubmenu("Sniffer")
	tools.AddText("Open Dashboard in Browser", keys.CmdOrCtrl("b"), func(*menu.CallbackData) {
		if a.srv != nil {
			runtime.BrowserOpenURL(a.ctx, a.srv.url())
		}
	})
	tools.AddText("Reload Dashboard", keys.CmdOrCtrl("r"), func(*menu.CallbackData) {
		runtime.WindowReloadApp(a.ctx)
	})
	tools.AddSeparator()
	tools.AddText("Show Data Folder", nil, func(*menu.CallbackData) {
		if a.srv != nil {
			reveal(a.srv.dataDir)
		}
	})
	tools.AddText("Show Host Log", nil, func(*menu.CallbackData) {
		if a.srv != nil {
			reveal(a.srv.logPath)
		}
	})
	appMenu.Append(menu.WindowMenu())

	err := wails.Run(&options.App{
		Title:            "Zigbee Sniffer",
		Width:            1440,
		Height:           960,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: options.NewRGB(13, 17, 23), // the dashboard's --bg
		AssetServer:      &assetserver.Options{Handler: http.HandlerFunc(a.bounce)},
		Menu:             appMenu,
		Mac: &mac.Options{About: &mac.AboutInfo{
			Title:   "Zigbee Sniffer",
			Message: "Zigbee/802.15.4 capture, diagnostics and 2.4 GHz spectrum — desktop shell for the zbsniff tethered host.",
		}},
		OnStartup:  a.startup,
		OnShutdown: a.shutdown,
	})
	if err != nil {
		log.Printf("wails: %v", err)
	}
	if a.srv != nil {
		a.srv.stop()
	}
}

func (a *app) startup(ctx context.Context) {
	a.ctx = ctx
	if a.srv == nil {
		return
	}
	// If the host dies after the dashboard is up, say so instead of leaving
	// the page silently stuck on "reconnecting".
	go func() {
		<-a.srv.exited
		if a.srv.stopping.Load() {
			return
		}
		select {
		case <-a.srv.ready:
		default:
			return // never came up: the bounce page already shows the error
		}
		runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "zbsniff host stopped",
			Message: fmt.Sprintf("The capture host exited (%s).\n\nLast log lines:\n%s", a.srv.exitReason(), a.srv.logTail(12)),
		})
		runtime.Quit(ctx)
	}()
}

func (a *app) shutdown(context.Context) {
	if a.srv != nil {
		a.srv.stop()
	}
}

// bounce is everything the Wails asset bridge serves: once the host answers,
// a page that navigates the window to it; on failure, the reason.
func (a *app) bounce(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if a.srv == nil {
		fmt.Fprint(w, errorPage("Couldn't start the zbsniff host", a.srvErr.Error(), ""))
		return
	}
	select {
	case <-a.srv.ready:
		target := a.srv.url() + r.URL.RequestURI()
		fmt.Fprintf(w, `<!doctype html><html><head><script>location.replace(%q)</script></head><body style="background:#0d1117"></body></html>`, target)
	case <-a.srv.exited:
		fmt.Fprint(w, errorPage("The zbsniff host exited during startup", a.srv.exitReason(), a.srv.logTail(20)))
	case <-time.After(30 * time.Second):
		fmt.Fprint(w, errorPage("The zbsniff host didn't answer within 30 s",
			"It may still be starting — use Sniffer → Reload Dashboard.", a.srv.logTail(20)))
	}
}

func errorPage(title, detail, logTail string) string {
	pre := ""
	if logTail != "" {
		pre = `<pre>` + html.EscapeString(logTail) + `</pre>`
	}
	return `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;padding:32px;background:#0d1117;color:#e6edf3;font:14px/1.5 system-ui,sans-serif}
h1{font-size:18px;color:#f85149}pre{background:#161b22;border:1px solid #2a3340;border-radius:8px;padding:12px;
font:12px/1.45 ui-monospace,Menlo,monospace;white-space:pre-wrap;color:#8b97a3}</style></head><body>
<h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(detail) + `</p>` + pre + `</body></html>`
}

// reveal shows a file or folder in the OS file manager.
func reveal(path string) {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", path)
	case "windows":
		cmd = exec.Command("explorer", "/select,", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	_ = cmd.Start()
}
