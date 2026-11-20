package app

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
)

// TrustExtraCACertificates adds the PEM certificates in NODE_EXTRA_CA_CERTS to the system
// roots that http.DefaultTransport trusts, as Node did for the TypeScript CLI, so a durin
// that worked behind a TLS-inspecting proxy keeps working. As in Node, a file that cannot be
// read or holds no certificate produces a warning on stderr and the system roots stay in use.
func TrustExtraCACertificates(stderr io.Writer) {
	path := os.Getenv("NODE_EXTRA_CA_CERTS")
	if path == "" {
		return
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	pem, err := os.ReadFile(path)
	pool, poolErr := x509.SystemCertPool()
	if poolErr != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		fmt.Fprintf(stderr, "Warning: Ignoring extra certs from `%s`, load failed: %s\n", TerminalText(path), certificateProblem(err))
		return
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSClientConfig != nil {
		config = transport.TLSClientConfig.Clone()
	}
	config.RootCAs = pool
	transport.TLSClientConfig = config
}

func certificateProblem(err error) string {
	if err != nil {
		return "error reading the file"
	}
	return "no certificate found"
}
