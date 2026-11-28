// Command ollupi serves an ollupi deployment: its gateway, the sessions
// behind it, and this machine as the sandbox they run in. The desktop app
// registers the address and a token as one of the servers it connects to.
//
//	OLLUPI_GATEWAY_TOKEN=... OLLUPI_OPERATOR_TOKEN=... ollupi serve --listen 127.0.0.1:7777
//
// A connection's token is a secret and a profile, joined by a dot:
// <secret>.<profile>. A store with no profile starts with the admin's, so
// the operator's token has an account to connect as from the first start.
// On start it prints the address it serves and the profiles it holds;
// SIGTERM or SIGINT stops it.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/sandbox/cloudflare"
	"github.com/0xfakeSpike/ollupi/server"
)

const (
	// AdminProfile is the profile a new store starts with, the one the
	// operator's token connects as. AdminName is the name people see it by.
	AdminProfile = "admin"
	AdminName    = "Admin"
)

// tokenSafe is what a secret needs to ride a WebSocket subprotocol, the only
// header a browser can set on the handshake. A hex secret always is; base64
// with "+", "/" or "=" may not be, and the browser then refuses the socket
// before the gateway sees the token.
var tokenSafe = regexp.MustCompile("^[!#$%&'*+^_`|~0-9A-Za-z-]+$")

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: ollupi serve [flags]")
		fmt.Fprintln(os.Stderr, "run `ollupi serve -h` for the flags")
		os.Exit(2)
	}
	os.Exit(serve(os.Args[2:]))
}

func serve(args []string) int {
	fs := flag.NewFlagSet("ollupi serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		home           = fs.String("home", defaultHome(), "the server's folder: its database, attachments and workspaces")
		listen         = fs.String("listen", "127.0.0.1:7777", "the address the gateway listens on")
		origins        = fs.String("allow-origin", "", "comma-separated WebSocket origin patterns to allow beyond loopback, e.g. ollupi for the desktop app")
		hosts          = fs.String("allow-host", "", "comma-separated Host names the gateway serves beyond localhost, e.g. ollupi.example.com")
		cert           = fs.String("tls-cert", "", "PEM certificate: with --tls-key the gateway serves wss://")
		key            = fs.String("tls-key", "", "PEM private key for --tls-cert")
		gatewayToken   = fs.String("gateway-token", "", "the gateway's secret (default OLLUPI_GATEWAY_TOKEN); a connection's token is it, a dot and a profile")
		operatorToken  = fs.String("operator-token", "", "the operator's secret (default OLLUPI_OPERATOR_TOKEN)")
		cloudflareURL  = fs.String("cloudflare-sandbox", "", "URL of a Cloudflare sandbox bridge this server offers (its token comes from SANDBOX_API_KEY; SANDBOX_API_URL also works)")
		cloudflareRoot = fs.String("cloudflare-root", "/workspace", "the folder of a Cloudflare sandbox an environment is rooted at")
		firstSandbox   = fs.String("default-sandbox", "", "the provider a new profile's first environment is of: local (default), or cloudflare when it is offered")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// The environment is preferred: an argument is visible to every other
	// process of the machine.
	secret := firstOf(*gatewayToken, os.Getenv("OLLUPI_GATEWAY_TOKEN"))
	operator := firstOf(*operatorToken, os.Getenv("OLLUPI_OPERATOR_TOKEN"))
	switch {
	case secret == "" || operator == "":
		slog.Error("serve: OLLUPI_GATEWAY_TOKEN and OLLUPI_OPERATOR_TOKEN must both be set")
		return 2
	case secret == operator:
		slog.Error("serve: the operator's token must differ from the gateway's, or every user would be the operator")
		return 2
	case (*cert == "") != (*key == ""):
		slog.Error("serve: --tls-cert and --tls-key go together")
		return 2
	}
	if !tokenSafe.MatchString(secret) || !tokenSafe.MatchString(operator) {
		slog.Warn("serve: a token holds characters a browser cannot carry in the WebSocket handshake; a hex secret, as openssl rand -hex 32 makes one, avoids that")
	}

	homeDir, err := filepath.Abs(*home)
	if err != nil {
		slog.Error("serve: the home", "error", err)
		return 1
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		slog.Error("serve: the home", "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A Cloudflare sandbox bridge is one more sandbox this deployment offers;
	// its token comes from the environment, never from an argument.
	sandboxes := map[string]sandbox.Provider{}
	if address := firstOf(*cloudflareURL, os.Getenv("SANDBOX_API_URL")); address != "" {
		p, err := cloudflare.New(cloudflare.Config{BaseURL: address, APIKey: os.Getenv("SANDBOX_API_KEY"), Root: *cloudflareRoot})
		if err != nil {
			slog.Error("serve: the Cloudflare sandbox", "error", err)
			return 2
		}
		sandboxes["cloudflare"] = p
	}

	h, err := newServer(ctx, homeDir, secret, operator, splitList(*origins), splitList(*hosts), sandboxes, *firstSandbox)
	if err != nil {
		slog.Error("serve: the server was not built", "error", err)
		return 1
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		h.Close()
		slog.Error("serve: the gateway cannot listen", "error", err)
		return 1
	}
	scheme := "ws"
	if *cert != "" {
		pair, err := tls.LoadX509KeyPair(*cert, *key)
		if err != nil {
			h.Close()
			slog.Error("serve: the certificate", "error", err)
			return 1
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
		scheme = "wss"
	}

	// The tokens are not printed: they are the ones the server was given.
	profiles := h.Profiles(ctx)
	example := AdminProfile
	if len(profiles) > 0 {
		example = profiles[0]
	}
	fmt.Fprintf(os.Stderr, "ollupi serve\n  address   %s://%s/\n  profiles  %s\n  tokens    a gateway token, a dot and a profile, e.g. <secret>.%s\n",
		scheme, ln.Addr(), strings.Join(profiles, ", "), example)

	if err := h.Serve(ctx, ln); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("serve: stopped", "error", err)
		return 1
	}
	return 0
}

// newServer builds the standalone deployment: the engine's shared server
// starting with the admin's profile.
func newServer(ctx context.Context, home, secret, operator string, origins, hosts []string, sandboxes map[string]sandbox.Provider, firstSandbox string) (*server.Server, error) {
	return server.New(ctx, server.Config{
		Home:           home,
		Token:          secret,
		OperatorToken:  operator,
		AllowedOrigins: origins,
		AllowedHosts:   hosts,
		Sandboxes:      sandboxes,
		Sandbox:        firstSandbox,
		First:          server.Profile{ID: AdminProfile, Name: AdminName},
	})
}

func defaultHome() string {
	if v := os.Getenv("OLLUPI_HOME"); v != "" {
		return v
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return ".ollupi"
	}
	return filepath.Join(dir, ".ollupi")
}

// firstOf is the first value that is not blank.
func firstOf(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
