package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routercontrol"
	"io"
	"os"
	"time"
)

const serviceHelp = `Usage: api-router service <start|stop|restart|status|autostart enable|autostart disable|autostart status>
start [--experimental-redaction|--no-experimental-redaction] starts a hidden per-user process.
Without a flag, start/restart/login autostart retain the last explicit trial choice.
Login autostart is off by default. Enable and disable are explicit Windows per-user choices.
No service operation enables, downloads or loads a model. stop preserves host configuration.
If the page is stopped, use the installed EXE with service start to open it again.
Status is JSON; address is the real loopback management address. No admin service is installed.
`

func runService(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return printHelp(out, serviceHelp)
	}
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "help" {
			return printHelp(out, serviceHelp)
		}
	}
	action := args[0]
	var trial *bool
	switch action {
	case "start":
		if len(args) == 2 {
			v := args[1] == "--experimental-redaction"
			if !v && args[1] != "--no-experimental-redaction" {
				return fmt.Errorf("invalid service start option; use service --help")
			}
			trial = &v
		} else if len(args) != 1 {
			return fmt.Errorf("invalid service start options")
		}
	case "stop", "restart", "status":
		if len(args) != 1 {
			return fmt.Errorf("unexpected service options")
		}
	case "autostart":
		if len(args) != 2 || (args[1] != "enable" && args[1] != "disable" && args[1] != "status") {
			return fmt.Errorf("use service autostart enable|disable|status")
		}
	default:
		return fmt.Errorf("unknown service command; use service --help")
	}
	l, err := dirs.Resolve()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result any
	switch action {
	case "start":
		result, err = routercontrol.Start(ctx, l, trial)
	case "stop":
		result, err = routercontrol.Stop(ctx, l)
	case "restart":
		result, err = routercontrol.Restart(ctx, l)
	case "status":
		result, err = routercontrol.Current(ctx, l)
	case "autostart":
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		if args[1] == "status" {
			result, err = routercontrol.AutostartStatus(l, exe)
		} else {
			if args[1] == "enable" {
				s, e := routercontrol.Current(ctx, l)
				if e != nil {
					return e
				}
				if s.Running {
					if e = routercontrol.SaveOptions(l, s.ExperimentalRedaction); e != nil {
						return e
					}
				}
			}
			result, err = routercontrol.SetAutostart(l, exe, args[1] == "enable")
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
func printHelp(out io.Writer, s string) error { _, err := io.WriteString(out, s); return err }
