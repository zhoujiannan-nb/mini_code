// Desktop mode wiring for package main: adapts the internal *runtime to the
// desktop shell (package desktop). The concrete platform shell is selected
// by newDesktopPlatform() in the platform-specific desktop_app_*.go files.
package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/user/mini_code/desktop"

	"github.com/user/mini_code/web"
)

// desktopRT adapts the internal *runtime to desktop.Runtime.
type desktopRT struct {
	rt *runtime
}

func (d desktopRT) CloseAll() {
	d.rt.closeClients()
	d.rt.mgr.Close()
}

func (d desktopRT) Handler() http.Handler { return web.NewServer(d.rt) }

func (d desktopRT) StartHub(ctx context.Context) { go d.rt.hub.Run(ctx) }

// runDesktop implements "mini_code desktop" (and the no-argument default of
// desktop builds) on Windows / macOS.
func runDesktop(args []string) {
	platform := newDesktopPlatform()
	if platform == nil {
		fatal(errors.New("desktop 模式仅支持 Windows / macOS（macOS 请用 build_darwin.sh 构建带壳版本）"))
	}
	desktop.Run(args, desktop.Deps{
		Platform: platform,
		TrayIcon: desktopTrayIcon,
		NewRuntime: func(rest []string) (desktop.Runtime, error) {
			rt, err := setup(rest, 0)
			if err != nil {
				return nil, err
			}
			return desktopRT{rt: rt}, nil
		},
	})
}
