package app

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A server signed by a private CA is trusted only once NODE_EXTRA_CA_CERTS names that CA; an
// unreadable file warns and leaves the system roots in place.
func TestTrustExtraCACertificates(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	fresh := func() *http.Transport {
		transport := original.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		http.DefaultTransport = transport
		return transport
	}

	fresh()
	if _, err := http.Get(server.URL); err == nil {
		t.Fatal("an untrusted CA was accepted")
	}

	path := filepath.Join(t.TempDir(), "ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_EXTRA_CA_CERTS", path)
	fresh()
	var stderr bytes.Buffer
	TrustExtraCACertificates(&stderr)
	if response, err := http.Get(server.URL); err != nil || stderr.Len() != 0 {
		t.Fatalf("extra CA not trusted: %v %q", err, stderr.String())
	} else {
		response.Body.Close()
	}

	t.Setenv("NODE_EXTRA_CA_CERTS", filepath.Join(t.TempDir(), "missing.pem"))
	fresh()
	stderr.Reset()
	TrustExtraCACertificates(&stderr)
	if !strings.Contains(stderr.String(), "Ignoring extra certs") {
		t.Fatalf("no warning: %q", stderr.String())
	}
}
