package fakeserver

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Run accepts the fixed b10828 process flags and explicit fake controls. exit
// is injected so unit tests can observe a crash without exiting their runner.
func Run(args []string, stderr io.Writer, exit func(int)) int {
	flags := flag.NewFlagSet("fake-llama-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "127.0.0.1", "loopback host")
	port := flags.Int("port", 8080, "port")
	flags.String("model", "", "ignored fixture model")
	flags.String("chat-template-file", "", "ignored verified fixture template")
	flags.Int("ctx-size", 4096, "context")
	flags.Int("parallel", 1, "slots")
	flags.Int("cache-reuse", 256, "KV reuse configuration")
	flags.Int("n-gpu-layers", 0, "GPU layers")
	flags.Bool("jinja", true, "template")
	startFail := flags.Bool("fake-start-fail", false, "exit before listening")
	unhealthy := flags.Bool("fake-unhealthy", false, "stay loading")
	status := flags.String("fake-health-status", "ok", "HTTP 200 health JSON status")
	loadDelay := flags.Duration("fake-load-delay", 0, "loading duration")
	crashAfter := flags.Duration("fake-crash-after", 0, "exit after this duration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *host != "127.0.0.1" || *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, "fake server requires an explicit loopback port and known flags")
		return 2
	}
	if *startFail {
		fmt.Fprintln(stderr, "controlled fake startup failure")
		return 22
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer listener.Close()
	started := time.Now()
	handler := New(Config{Status: *status, Ready: func() bool { return !*unhealthy && time.Since(started) >= *loadDelay },
		Crash: func() { exit(23) }})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	if *crashAfter > 0 {
		timer := time.AfterFunc(*crashAfter, func() { exit(23) })
		defer timer.Stop()
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// Main runs the command with real process-exit semantics.
func Main() { os.Exit(Run(os.Args[1:], os.Stderr, os.Exit)) }
