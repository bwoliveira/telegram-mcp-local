// Command gen builds the static Telegram MCP site from site/src into a
// deployable directory. It uses only the standard library. See site/DESIGN.md.
//
//	go run ./site/gen -out site/dist                         # pre-launch build (default)
//	go run ./site/gen -out site/dist -serve 127.0.0.1:8088   # build, then serve with _headers
//	go run ./site/gen -out site/dist -mode release -version v1.0.0   # Stage 6 only
//	go run ./site/gen -og-html DIR                            # sources for tools/render-images.sh
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	var (
		printHeaders = flag.Bool("print-security-headers", false, "print expected global security headers as JSON and exit")
		src          = flag.String("src", "site/src", "source directory")
		out          = flag.String("out", "site/dist", "output directory (replaced on every build)")
		mode         = flag.String("mode", ModePrelaunch, "build mode: prelaunch or release")
		version      = flag.String("version", "", "release version, for example v1.0.0 (release mode only)")
		date         = flag.String("date", "", "release date YYYY-MM-DD shown on /download/ (release mode, optional)")
		baseURL      = flag.String("base-url", "", "override baseURL from site.json")
		serve        = flag.String("serve", "", "after building, serve the output on this loopback address")
		ogHTML       = flag.String("og-html", "", "write the HTML sources of the social images to this directory and exit")
	)
	flag.Parse()
	if *printHeaders {
		values := map[string]string{}
		for _, rule := range parseHeaders(Headers(ModeRelease)) {
			if rule.pattern == "/*" {
				for _, kv := range rule.set {
					values[kv[0]] = kv[1]
				}
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *ogHTML != "" {
		if err := WriteImageSources(*src, *ogHTML); err != nil {
			log.Fatal(err)
		}
		fmt.Println("image sources written to", *ogHTML)
		return
	}

	opts := Options{Src: *src, Out: *out, Mode: *mode, Version: *version, Date: *date, BaseURL: *baseURL}
	res, err := Build(opts)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("built %d pages (%s mode) into %s\n", res.Pages, opts.Mode, opts.Out)

	if *serve == "" {
		return
	}
	if err := checkLoopback(*serve); err != nil {
		log.Fatal(err)
	}
	h, err := NewStaticHandler(opts.Out)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *serve, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("serving %s at http://%s/ with _headers applied (Ctrl+C to stop)\n", opts.Out, *serve)
	log.Fatal(srv.ListenAndServe())
}

// checkLoopback refuses to serve the preview on anything but a loopback
// address: the local server is for QA, never for publishing.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("-serve only accepts a loopback address such as 127.0.0.1:8088")
	}
	return nil
}

// exitf is used by helpers that cannot return errors to main.
func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
