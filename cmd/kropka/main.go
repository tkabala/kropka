// Command kropka serves a directory as a mobile-friendly gallery.
//
//	kropka            # serve the current directory on localhost:8080
//	kropka --lan ~/x  # serve ~/x to your local network, with a QR code
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/tkabala/kropka/internal/auth"
	"github.com/tkabala/kropka/internal/fsview"
	"github.com/tkabala/kropka/internal/server"
	"github.com/tkabala/kropka/internal/thumb"
	"github.com/tkabala/kropka/internal/ui"
	"github.com/tkabala/kropka/internal/watch"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

type options struct {
	port     int
	bind     string
	lan      bool
	noAuth   bool
	token    string
	hidden   bool
	qr       bool
	quiet    bool
	version  bool
	noThumbs bool
	cacheDir string
	ffmpeg   string
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "kropka:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	var o options
	fs := flag.NewFlagSet("kropka", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&o.port, "port", envInt("KROPKA_PORT", 8080), "port to listen on (next free port is used if busy)")
	fs.IntVar(&o.port, "p", envInt("KROPKA_PORT", 8080), "shorthand for --port")
	fs.StringVar(&o.bind, "bind", os.Getenv("KROPKA_BIND"), "address to bind (default 127.0.0.1, or 0.0.0.0 with --lan)")
	fs.BoolVar(&o.lan, "lan", false, "listen on all interfaces and print a QR code for your phone")
	fs.BoolVar(&o.noAuth, "no-auth", os.Getenv("KROPKA_NO_AUTH") == "1", "disable the access token (not recommended with --lan)")
	fs.StringVar(&o.token, "token", os.Getenv("KROPKA_TOKEN"), "use a fixed access token instead of a random one")
	fs.BoolVar(&o.hidden, "hidden", false, "show dotfiles and dot-directories")
	fs.BoolVar(&o.qr, "qr", false, "print a QR code even without --lan")
	fs.BoolVar(&o.quiet, "quiet", false, "do not log requests")
	fs.BoolVar(&o.noThumbs, "no-thumbs", false, "show original images in the grid instead of generating thumbnails")
	fs.StringVar(&o.cacheDir, "cache-dir", os.Getenv("KROPKA_CACHE_DIR"), "where to keep thumbnails (default: the user cache dir)")
	fs.StringVar(&o.ffmpeg, "ffmpeg", os.Getenv("KROPKA_FFMPEG"), `ffmpeg for video thumbnails, or "off" (default: ffmpeg on PATH, if any)`)
	fs.BoolVar(&o.version, "version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "kropka %s — serve . to your phone\n\nUsage: kropka [flags] [dir]\n\nFlags:\n", version)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.version {
		fmt.Fprintln(stdout, "kropka", version)
		return nil
	}

	dir := "."
	switch fs.NArg() {
	case 0:
	case 1:
		dir = fs.Arg(0)
	default:
		return errors.New("expected at most one directory")
	}

	root, err := fsview.Open(dir, o.hidden)
	if err != nil {
		return err
	}
	defer root.Close()

	var thumbs *thumb.Service
	if !o.noThumbs {
		thumbs, err = openThumbs(o.cacheDir, root)
		if err != nil {
			// Not fatal: the grid falls back to the originals.
			fmt.Fprintln(stderr, "kropka: thumbnails disabled:", err)
		} else if ffmpeg, err := findFFmpeg(o.ffmpeg); err != nil {
			fmt.Fprintln(stderr, "kropka: video thumbnails disabled:", err)
		} else {
			thumbs.SetFFmpeg(ffmpeg)
		}
	}

	watcher, err := watch.New(root)
	if err != nil {
		// Not fatal: the page still reloads on refresh and when refocused.
		fmt.Fprintln(stderr, "kropka: live reload disabled:", err)
		watcher = nil
	} else {
		defer watcher.Close()
	}

	token := o.token
	if !o.noAuth && token == "" {
		if token, err = auth.NewToken(); err != nil {
			return err
		}
	}
	if o.noAuth {
		token = ""
	}

	bind := o.bind
	if bind == "" {
		bind = "127.0.0.1"
		if o.lan {
			bind = "0.0.0.0"
		}
	}

	ln, port, err := listen(bind, o.port)
	if err != nil {
		return err
	}

	var logger *log.Logger
	if !o.quiet {
		logger = log.New(stderr, "", log.Ltime)
	}
	srv := &http.Server{
		Handler: server.New(server.Config{
			Root:    root,
			Thumbs:  thumbs,
			Watch:   watcher,
			Token:   token,
			UI:      ui.FS(),
			Version: version,
			Logger:  logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if watcher != nil {
		// Event streams never go idle on their own; closing the watcher ends
		// them so Shutdown doesn't wait out its timeout.
		srv.RegisterOnShutdown(func() { watcher.Close() })
	}

	printBanner(stdout, root.Dir(), bind, port, token, o.lan || o.qr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	fmt.Fprintln(stdout, "\nbye.")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func openThumbs(dir string, root *fsview.Root) (*thumb.Service, error) {
	if dir == "" {
		var err error
		if dir, err = thumb.DefaultDir(); err != nil {
			return nil, err
		}
	}
	return thumb.New(root, dir)
}

// findFFmpeg resolves --ffmpeg: a path or a name to look up on PATH, "off",
// or empty for "ffmpeg" if there is one. Only a missing ffmpeg that was asked
// for by name is an error; without one, videos keep their <video> posters.
func findFFmpeg(flag string) (string, error) {
	switch flag {
	case "off":
		return "", nil
	case "":
		p, _ := exec.LookPath("ffmpeg")
		return p, nil
	}
	return exec.LookPath(flag)
}

// listen binds to port, or the next free one (up to 20 tries) if it is taken.
func listen(bind string, port int) (net.Listener, int, error) {
	var lastErr error
	for p := port; p < port+20; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(p)))
		if err == nil {
			return ln, ln.Addr().(*net.TCPAddr).Port, nil
		}
		lastErr = err
		if port == 0 {
			break
		}
	}
	return nil, 0, lastErr
}

func printBanner(w io.Writer, dir, bind string, port int, token string, showQR bool) {
	fmt.Fprintf(w, "\n  ● kropka %s\n  serving %s\n\n", version, dir)

	urls := []string{}
	if bind == "0.0.0.0" || bind == "::" {
		urls = append(urls, makeURL("localhost", port, token))
		for _, ip := range lanIPs() {
			urls = append(urls, makeURL(ip, port, token))
		}
	} else {
		host := bind
		if host == "127.0.0.1" {
			host = "localhost"
		}
		urls = append(urls, makeURL(host, port, token))
	}
	for _, u := range urls {
		fmt.Fprintf(w, "  → %s\n", u)
	}

	if bind == "127.0.0.1" {
		fmt.Fprintf(w, "\n  Remote server? Forward the port from your machine:\n    ssh -L %d:localhost:%d <server>\n  then open the link above. Use --lan to expose it on the network instead.\n", port, port)
	}
	if token == "" {
		fmt.Fprintln(w, "\n  ! access token disabled (--no-auth): anyone who can reach this port can read the files")
	}

	if showQR {
		target := urls[len(urls)-1] // a LAN address if there is one
		if q, err := qrcode.New(target, qrcode.Low); err == nil {
			fmt.Fprintf(w, "\n%s  %s\n", indent(q.ToSmallString(false), "  "), target)
		}
	}
	fmt.Fprintln(w, "\n  Ctrl+C to stop")
	fmt.Fprintln(w)
}

func makeURL(host string, port int, token string) string {
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/"}
	if token != "" {
		u.RawQuery = auth.QueryParam + "=" + token
	}
	return u.String()
}

func lanIPs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		// FlagUp alone is only the admin state: a bridge with nothing attached
		// (docker0) is UP but has no carrier. FlagRunning is the operational state.
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagRunning == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

func indent(s, prefix string) string {
	out := prefix
	for i, r := range s {
		out += string(r)
		if r == '\n' && i < len(s)-1 {
			out += prefix
		}
	}
	return out
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
