package app

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/weburl"
)

// browserOpener is the platform command that opens url in the default browser.
func browserOpener(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	}
	return "xdg-open", []string{url}
}

// openBrowserCommand runs a browser opener; tests replace it.
var openBrowserCommand = func(ctx context.Context, command string, args []string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, command, args...).Run()
}

// OpenBrowser hands an http(s) URL to the platform's opener, waiting at most five seconds and
// ignoring its failure: the URL is also shown in the terminal (src/browser.ts). Any other URL
// is refused, so a server cannot make it launch a local program or file.
func OpenBrowser(ctx context.Context, url string) error {
	parsed, err := weburl.Parse(url)
	if err != nil || (parsed.Protocol() != "https:" && parsed.Protocol() != "http:") {
		return clierror.New("Refusing to open a non-web authorization URL.")
	}
	command, args := browserOpener(runtime.GOOS, url)
	openBrowserCommand(ctx, command, args)
	return nil
}
