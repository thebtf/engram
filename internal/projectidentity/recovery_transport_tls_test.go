package projectidentity

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecoveryTransportHonorsTLSCA(t *testing.T) {
	var fixtureRoot string
	t.Cleanup(func() {
		if _, err := os.Stat(fixtureRoot); !os.IsNotExist(err) {
			t.Error("test-owned certificate and caller directory was not removed")
		} else {
			t.Log("cleanup: test-owned CA files and caller repository removed; private keys were memory-only")
		}
	})
	fixtureRoot = t.TempDir()
	callerRoot := filepath.Join(fixtureRoot, "caller")
	if err := os.Mkdir(callerRoot, 0o700); err != nil {
		t.Fatal("create test-owned caller directory")
	}
	gitLogical(t, callerRoot, "init", "--quiet", "--template=")
	certificate, correctCA := recoveryTransportCertificate(t, fixtureRoot, "trusted")
	_, wrongCA := recoveryTransportCertificate(t, fixtureRoot, "unrelated")
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		t.Fatal("generate synthetic credential")
	}
	token := hex.EncodeToString(tokenBytes[:])

	for _, operation := range []string{"legacy-lookup", "project-choices"} {
		t.Run(operation, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				scheme     string
				ca         string
				tlsServer  bool
				wantRead   bool
				wantTLS    bool
			}{
				{"https-trusted-ca", "https", correctCA, true, true, true},
				{"http-ca-overrides-scheme", "http", correctCA, true, true, true},
				{"https-wrong-ca-no-fallback", "https", wrongCA, true, false, true},
				{"http-wrong-ca-no-fallback", "http", wrongCA, true, false, true},
				{"http-ca-cleartext-trap", "http", correctCA, false, false, true},
				{"http-no-ca-supported", "http", "", false, true, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Setenv("ENGRAM_TLS_CA", tc.ca)
					address, stop := recoveryTransportServer(t, certificate, tc.tlsServer, token)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					endpoint := tc.scheme + "://" + address
					var readOK, emptyResult bool
					var err error
					if operation == "legacy-lookup" {
						var project Project
						project, err = lookupLegacyProject(ctx, endpoint, token, "941b49")
						readOK = project == (Project{ProjectID: "11111111-1111-4111-8111-111111111111", Name: "TLS fixture"})
						emptyResult = project == (Project{})
					} else {
						var choices ProjectChoices
						choices, err = ProjectContextChoices(ctx, ProjectContextCaller{Cwd: callerRoot, ServerURL: endpoint, Token: token})
						readOK = len(choices.Choices) == 1 && choices.Choices[0].ProjectID == "11111111-1111-4111-8111-111111111111" && choices.Choices[0].Name == "TLS fixture" && choices.Choices[0].SelectionRef != ""
						emptyResult = len(choices.Choices) == 0
					}
					observed := stop()
					// Never log credentials, raw requests or transport errors: the latter
					// may acquire sensitive request details in a future implementation.
					t.Logf("read_ok=%t error_code=%q tls_attempts=%d tls_reads=%d plaintext_reads=%d plaintext_bearer_observed=%t", readOK, ProjectErrorCode(err), observed.tlsAttempts, observed.tlsReads, observed.plaintextReads, observed.plaintextBearer)
					if observed.contractInvalid {
						t.Error("actual request violated the existing method, route, query, bearer or cookie contract")
					}
					if tc.wantRead {
						if err != nil || !readOK {
							t.Error("configured transport did not read the real authorized project response")
						}
					} else if ProjectErrorCode(err) != "PROJECT_RESOLUTION_UNAVAILABLE" || !emptyResult {
						t.Error("failed trust or TLS-only cleartext trap did not fail closed with an empty result")
					}
					if tc.wantTLS {
						if observed.tlsAttempts == 0 || observed.plaintextReads != 0 || observed.plaintextBearer {
							t.Error("custom CA policy allowed plaintext instead of TLS; plaintext bearer observation is recorded only as a boolean")
						}
						if tc.wantRead && observed.tlsReads != 1 || !tc.wantRead && observed.tlsReads != 0 {
							t.Error("authenticated TLS request count does not match the trust decision")
						}
					} else if observed.tlsAttempts != 0 || observed.plaintextReads != 1 || !observed.plaintextBearer {
						t.Error("ordinary explicitly configured local HTTP without a CA stopped working")
					}
				})
			}
		})
	}
}

func recoveryTransportCertificate(t *testing.T, directory, label string) (tls.Certificate, string) {
	t.Helper()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate test-owned CA key")
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Recovery transport " + label},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caPrivate)
	if err != nil {
		t.Fatal("sign test-owned CA")
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal("parse test-owned CA")
	}
	caPath := filepath.Join(directory, label+"-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal("write test-owned CA certificate")
	}
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate loopback server key")
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Recovery loopback"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, serverPublic, caPrivate)
	if err != nil {
		t.Fatal("sign loopback server certificate")
	}
	leaf, err = x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal("parse loopback server certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"}); err != nil {
		t.Fatal("synthetic certificate does not validate for its own CA and loopback address")
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: serverPrivate, Leaf: leaf}, caPath
}

type recoveryTransportObservation struct {
	tlsAttempts     int
	tlsReads        int
	plaintextReads  int
	plaintextBearer bool
	contractInvalid bool
}

// The TLS fixture also accepts cleartext on the exact same socket so an unsafe
// retry cannot hide behind a TLS server rejecting plaintext before HTTP parsing.
// The cleartext-only variant closes ClientHello without reading any HTTP header.
func recoveryTransportServer(t *testing.T, certificate tls.Certificate, allowTLS bool, token string) (string, func() recoveryTransportObservation) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("open synthetic loopback listener")
	}
	var mu sync.Mutex
	var observed recoveryTransportObservation
	connections := make(map[net.Conn]bool)
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[connection] = true
			mu.Unlock()
			workers.Add(1)
			go func(connection net.Conn) {
				defer workers.Done()
				defer func() {
					connection.Close()
					mu.Lock()
					delete(connections, connection)
					mu.Unlock()
				}()
				if connection.SetDeadline(time.Now().Add(5 * time.Second)) != nil {
					return
				}
				reader := bufio.NewReader(connection)
				first, err := reader.Peek(1)
				if err != nil {
					return
				}
				isTLS := first[0] == 0x16
				var stream net.Conn = connection
				if isTLS {
					mu.Lock()
					observed.tlsAttempts++
					mu.Unlock()
					if !allowTLS {
						return
					}
					tlsConnection := tls.Server(&recoveryTransportBufferedConn{Conn: connection, reader: reader}, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
					if tlsConnection.Handshake() != nil {
						return
					}
					stream = tlsConnection
					reader = bufio.NewReader(stream)
				}
				request, err := http.ReadRequest(reader)
				if err != nil {
					return
				}
				request.Body.Close()
				authorization := request.Header.Get("Authorization")
				body := ""
				switch {
				case request.URL.Path == "/api/projects/resolve" && request.URL.RawQuery == "legacy_id=941b49":
					body = `{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"TLS fixture"}`
				case request.URL.Path == "/api/projects" && request.URL.RawQuery == "":
					body = `{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"TLS fixture"}],"unresolved":[]}`
				}
				mu.Lock()
				if isTLS {
					observed.tlsReads++
				} else {
					observed.plaintextReads++
					observed.plaintextBearer = observed.plaintextBearer || strings.HasPrefix(authorization, "Bearer ") && strings.Contains(authorization, token)
				}
				invalid := request.Method != http.MethodGet || authorization != "Bearer "+token || request.Header.Get("Cookie") != "" || body == ""
				observed.contractInvalid = observed.contractInvalid || invalid
				mu.Unlock()
				if invalid {
					_, _ = fmt.Fprint(stream, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
					return
				}
				_, _ = fmt.Fprintf(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}(connection)
		}
	}()
	var once sync.Once
	stop := func() recoveryTransportObservation {
		once.Do(func() {
			listener.Close()
			<-acceptDone
			mu.Lock()
			for connection := range connections {
				connection.Close()
			}
			mu.Unlock()
			workers.Wait()
		})
		mu.Lock()
		defer mu.Unlock()
		return observed
	}
	t.Cleanup(func() {
		stop()
		t.Log("cleanup: loopback listener and accepted sockets closed; server goroutines joined")
	})
	return listener.Addr().String(), stop
}

type recoveryTransportBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *recoveryTransportBufferedConn) Read(buffer []byte) (int, error) {
	return connection.reader.Read(buffer)
}
