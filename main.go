// Plex Collection Manager — Go build (v1.0.0).
//
// FUTURE AI: this is the Go port of the owner's Electron app (see HANDOFF.md
// in this tree for the full story). One static binary: it serves the SAME
// web UI the Electron renderer used, from an embedded filesystem, and the
// UI talks to the Go backend over /api/* (web/bridge.js replaces
// Electron's preload.js; the renderer files are otherwise near-identical).
//
// SECURITY MODEL — read before "improving":
//   - The HTTP server binds 127.0.0.1 ONLY. Nothing here is reachable
//     from the LAN or the internet.
//   - A random per-launch session token guards every /api/* route (the UI
//     receives it via /session.js; images/SSE carry it as ?t=). Loopback
//   - token means a random local process or a stray browser tab can't
//     drive the API against the owner's Plex server.
//   - Plex tokens and API keys live in internal/store (DPAPI on Windows)
//     and are NEVER served to the frontend — status booleans only.
//   - The Plex write policy (internal/plex/safety.go) is the same guard
//     the Electron app shipped: deletion is disabled, collection edits
//     require collection-type proof, poster writes require a verified
//     backup. Do not weaken it.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"plexcollectionmanager/internal/api"
)

// version is the Go build's version. The Electron line ended at 0.4.4;
// this port starts at 1.0.0.
const version = "1.23.2"

//go:embed web
var webEmbed embed.FS

//go:embed assets/badges
var badgesEmbed embed.FS

//go:embed assets/collection-art.json
var catalogJSON []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "plex-collection-manager:", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}

	webFS, err := fs.Sub(webEmbed, "web")
	if err != nil {
		return err
	}
	badgesFS, err := fs.Sub(badgesEmbed, "assets/badges")
	if err != nil {
		return err
	}

	token, err := newSessionToken()
	if err != nil {
		return err
	}

	srv, err := api.NewServer(api.Config{
		DataDir:      dataDir,
		Version:      version,
		SessionToken: token,
		WebFS:        webFS,
		BadgesFS:     badgesFS,
		CatalogJSON:  catalogJSON,
	})
	if err != nil {
		return err
	}

	// Port 38234 by default (same "unusual but fixed" spirit as the app's
	// other constants); if something already owns it, fall back to any
	// free port rather than failing to launch.
	ln, err := net.Listen("tcp", "127.0.0.1:38234")
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())

	fmt.Printf("Plex Collection Manager %s\n", version)
	fmt.Printf("Data directory: %s\n", dataDir)
	fmt.Printf("Open: %s\n", url)
	openBrowser(url) // best-effort; failure is fine, the URL is printed

	server := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server.Serve(ln)
}

// resolveDataDir matches the Electron app's userData location so existing
// settings carry over: %APPDATA%\Plex Collection Manager on Windows,
// ~/.config/Plex Collection Manager on Linux, ~/Library/Application
// Support/Plex Collection Manager on macOS. PCM_DATA_DIR overrides it
// (used by tests and the sandbox smoke run).
func resolveDataDir() (string, error) {
	if v := os.Getenv("PCM_DATA_DIR"); v != "" {
		return v, os.MkdirAll(v, 0o755)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "Plex Collection Manager")
	return dir, os.MkdirAll(dir, 0o755)
}

// newSessionToken returns 32 random bytes, hex-encoded. Per launch: a
// restart invalidates every open tab's token, which is acceptable for a
// single-user local app and keeps the model simple.
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// openBrowser makes a best-effort attempt to open the UI in the default
// browser (the double-click experience the owner expects). Any failure is
// non-fatal — the URL is printed either way.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
