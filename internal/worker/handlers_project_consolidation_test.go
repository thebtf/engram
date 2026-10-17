package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProjectConsolidationMultipartTermination(t *testing.T) {
	for _, extra := range []bool{false, true} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("sealed_export", "private.sealed")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte("sealed fixture")); err != nil {
			t.Fatal(err)
		}
		if extra {
			if err = writer.WriteField("foreign_part", "not allowed"); err != nil {
				t.Fatal(err)
			}
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(&body, writer.Boundary())
		export, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		stream := &consolidationMultipartStream{part: export, reader: reader}
		raw, err := io.ReadAll(stream)
		if extra {
			if err == nil {
				t.Fatal("foreign part accepted before restore commit")
			}
		} else if err != nil || string(raw) != "sealed fixture" {
			t.Fatal("complete sealed part rejected")
		}
	}
	response := httptest.NewRecorder()
	stream := &consolidationHTTPStream{ResponseWriter: response}
	if stream.started {
		t.Fatal("response committed before source validation")
	}
	if _, err := stream.Write([]byte("sealed frame")); err != nil {
		t.Fatal(err)
	}
	if !stream.started || response.Header().Get("Content-Type") != "application/octet-stream" || response.Header().Get("Trailer") != "X-Engram-Backup-Error" {
		t.Fatal("stream response contract missing")
	}
}

type consolidationTrackedBody struct {
	io.Reader
	readBytes int64
	maxRead   int
	closed    bool
	closeOnce sync.Once
}

func (body *consolidationTrackedBody) Close() error {
	body.closeOnce.Do(func() { body.closed = true })
	return nil
}

func (body *consolidationTrackedBody) Read(raw []byte) (int, error) {
	if len(raw) > body.maxRead {
		body.maxRead = len(raw)
	}
	n, err := body.Reader.Read(raw)
	body.readBytes += int64(n)
	return n, err
}

func TestProjectConsolidationMultipartMetadataOrderAndCleanup(t *testing.T) {
	for _, fields := range [][][2]string{
		{{"plan_fingerprint", "sha256:fixture"}, {"plan_reference", "fixture"}},
		{{"plan_reference", "fixture"}, {"plan_reference", "duplicate"}},
		{{"plan_reference", ""}, {"plan_fingerprint", "sha256:fixture"}},
		{{"plan_reference", "fixture"}, {"plan_fingerprint", strings.Repeat("x", 257)}},
		{{"plan_reference", "fixture"}, {"plan_fingerprint", "sha256:fixture"}},
	} {
		var encoded bytes.Buffer
		writer := multipart.NewWriter(&encoded)
		for _, field := range fields {
			if err := writer.WriteField(field[0], field[1]); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.WriteField("sealed_export", "sealed fixture"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		body := &consolidationTrackedBody{Reader: &encoded}
		request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/verify-backup", body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		(&Service{}).handleProjectConsolidationVerifyBackup(response, request)
		valid := fields[0][1] == "fixture" && fields[1][0] == "plan_fingerprint" && len(fields[1][1]) <= 256
		want := http.StatusBadRequest
		if valid {
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Fatalf("unexpected response status: %d", response.Code)
		}
		if !body.closed {
			t.Fatal("request stream retained after refusal")
		}
	}
}

type consolidationVirtualReader struct {
	remaining int64
	fill      byte
}

func (reader *consolidationVirtualReader) Read(raw []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(raw)), reader.remaining))
	if reader.fill == 0 {
		clear(raw[:n])
	} else {
		for i := range raw[:n] {
			raw[i] = reader.fill
		}
	}
	reader.remaining -= int64(n)
	return n, nil
}

func consolidationPolicyBody(t *testing.T, size int64, fields [][2]string, extra bool) (*consolidationTrackedBody, string, int64) {
	t.Helper()
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.CreateFormFile("sealed_export", "fixture.sealed"); err != nil {
		t.Fatal(err)
	}
	prefixSize := encoded.Len()
	if extra {
		if err := writer.WriteField("foreign_part", "not allowed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw := encoded.Bytes()
	body := &consolidationTrackedBody{Reader: io.MultiReader(bytes.NewReader(raw[:prefixSize]), &consolidationVirtualReader{remaining: size}, bytes.NewReader(raw[prefixSize:]))}
	return body, writer.FormDataContentType(), int64(len(raw)) + size
}

func consolidationPolicyService(t *testing.T, verifier projectConsolidationVerifier) *Service {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	tokenAuth, err := NewTokenAuth("registered-policy-test-only")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{router: chi.NewRouter(), tokenAuth: tokenAuth, rateLimiter: NewPerClientRateLimiter(0, 100), projectConsolidationVerifierSeam: verifier}
	service.ready.Store(true)
	service.setupMiddleware()
	service.setupRoutes()
	return service
}

func TestProjectConsolidationRegisteredPolicy(t *testing.T) {
	const path = "/api/projects/consolidation/verify-backup"
	validFields := [][2]string{{"plan_reference", "fixture"}, {"plan_fingerprint", "sha256:fixture"}}
	for _, tc := range []struct {
		name, method, path, contentType, message string
		size                                     int64
		fields                                   [][2]string
		chunked, noAuth, extra, declaredOver     bool
		wantStatus                               int
		wantVerifier                             bool
	}{
		{name: "small ordered multipart", size: 32, wantStatus: 200, wantVerifier: true},
		{name: "full framed size proxy streams", size: 859 << 20, wantStatus: 200, wantVerifier: true},
		{name: "chunked above ordinary cap", size: 11 << 20, chunked: true, wantStatus: 200, wantVerifier: true},
		{name: "declared finite cap", size: 32, declaredOver: true, wantStatus: 413, message: "request body too large"},
		{name: "chunked finite cap", size: projectConsolidationVerifyBackupMaxBytes + 1, chunked: true, wantStatus: 413, wantVerifier: true, message: "request body too large"},
		{name: "JSON not multipart", contentType: "application/json", wantStatus: 415, message: "multipart/form-data"},
		{name: "missing content type", contentType: "none", wantStatus: 415, message: "multipart/form-data"},
		{name: "missing boundary", contentType: "multipart/form-data", wantStatus: 415, message: "boundary"},
		{name: "wrong multipart type", contentType: "multipart/mixed; boundary=fixture", wantStatus: 415},
		{name: "different method", method: http.MethodPut, wantStatus: 415, message: "application/json"},
		{name: "other consolidation operation", path: "/api/projects/consolidation/backup", wantStatus: 415, message: "application/json"},
		{name: "trailing slash is not exempt", path: path + "/", wantStatus: 415, message: "application/json"},
		{name: "child path is not exempt", path: path + "/extra", wantStatus: 415, message: "application/json"},
		{name: "metadata order", fields: [][2]string{{"plan_fingerprint", "sha256:fixture"}, {"plan_reference", "fixture"}}, wantStatus: 400, message: "metadata order"},
		{name: "JSON metadata part rejected", fields: [][2]string{{"metadata", `{"plan_reference":"fixture"}`}}, wantStatus: 400, message: "metadata order"},
		{name: "empty metadata", fields: [][2]string{{"plan_reference", ""}, {"plan_fingerprint", "sha256:fixture"}}, wantStatus: 400, message: "invalid backup metadata"},
		{name: "metadata bound", fields: [][2]string{{"plan_reference", "fixture"}, {"plan_fingerprint", strings.Repeat("x", 257)}}, wantStatus: 400, message: "metadata too large"},
		{name: "extra part refused", size: 32, extra: true, wantStatus: 409, wantVerifier: true, message: "PROJECT_ADOPTION_INVENTORY_INCOMPLETE"},
		{name: "authentication still required", size: 32, noAuth: true, wantStatus: 401, message: "unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := tc.fields
			if fields == nil {
				fields = validFields
			}
			body, contentType, length := consolidationPolicyBody(t, tc.size, fields, tc.extra)
			called, released := false, false
			service := consolidationPolicyService(t, func(ctx context.Context, reference, fingerprint string, src io.Reader) (string, error) {
				called = true
				defer func() { released = true }()
				identity, ok := auth.IdentityFrom(ctx)
				if !ok || !identity.IsAdmin() || identity.Source != auth.SourceMaster {
					t.Fatal("verifier did not receive the authenticated data-admin identity")
				}
				if reference != "fixture" || fingerprint != "sha256:fixture" || body.readBytes > 8192 {
					t.Fatal("metadata changed or request was buffered before verifier entry")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > projectConsolidationOperationTimeout || time.Until(deadline) <= DefaultHTTPTimeout {
					t.Fatal("registered route timeout was bypassed")
				}
				n, err := io.Copy(io.Discard, src)
				if tc.wantStatus == http.StatusRequestEntityTooLarge {
					var tooLarge *http.MaxBytesError
					if !errors.As(err, &tooLarge) || tooLarge.Limit != projectConsolidationVerifyBackupMaxBytes || n <= 10<<20 {
						t.Fatalf("finite stream limit did not produce a typed refusal: bytes=%d, err=%v", n, err)
					}
					// Real frame decoding translates transport errors into this refusal.
					return "", &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_BACKUP_TRUNCATED"}
				}
				if err != nil {
					return "", err
				}
				if n != tc.size {
					t.Fatalf("stream received %d bytes, want %d", n, tc.size)
				}
				return "verified-fixture", nil
			})
			method, target := tc.method, tc.path
			if method == "" {
				method = http.MethodPost
			}
			if target == "" {
				target = path
			}
			request := httptest.NewRequest(method, target, body)
			request.ContentLength = length
			if tc.chunked {
				request.ContentLength = -1
				request.TransferEncoding = []string{"chunked"}
			}
			if tc.declaredOver {
				request.ContentLength = projectConsolidationVerifyBackupMaxBytes + 1
			}
			if tc.contentType != "" {
				contentType = tc.contentType
			}
			if contentType != "none" {
				request.Header.Set("Content-Type", contentType)
			}
			if !tc.noAuth {
				request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
			}
			request.Header.Set("Origin", "http://localhost:3000")
			request.Header.Set("X-Request-ID", "stream-policy-fixture")
			response := httptest.NewRecorder()
			service.router.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || called != tc.wantVerifier || released != called {
				t.Fatalf("status=%d, verifier=%v, released=%v; want status=%d, verifier=%v; response=%s", response.Code, called, released, tc.wantStatus, tc.wantVerifier, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), tc.message) {
				t.Fatalf("missing policy refusal %q: %s", tc.message, response.Body.String())
			}
			if isProjectConsolidationVerifyBackup(request) && !body.closed {
				t.Fatal("verification transport was not closed on completion/refusal")
			}
			if tc.wantStatus == http.StatusOK {
				var result map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result["backup_reference"] != "verified-fixture" || result["restore"] != "verified_isolated_database" {
					t.Fatalf("invalid verifier HTTP response: %s", response.Body.String())
				}
			}
			if tc.chunked && tc.wantStatus == http.StatusRequestEntityTooLarge && body.readBytes != projectConsolidationVerifyBackupMaxBytes+1 {
				t.Fatalf("finite transport read %d bytes", body.readBytes)
			}
			if tc.declaredOver && body.readBytes != 0 {
				t.Fatal("declared over-limit body was read before refusal")
			}
			if body.maxRead > 64<<10 {
				t.Fatalf("unbounded transport read: %d bytes", body.maxRead)
			}
			if response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" || response.Header().Get("X-Request-ID") != "stream-policy-fixture" {
				t.Fatal("registered security/CORS/trace middleware was bypassed")
			}
			_ = request.Body.Close()
		})
	}
}

func TestProjectConsolidationRegisteredOrdinaryJSONPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		size              int64
		chunked           bool
		wantStatus        int
		message           string
	}{
		{"ordinary JSON reaches data guard", "application/json", 0, false, 409, "PROJECT_ADOPTION_INVENTORY_INCOMPLETE"},
		{"ordinary non-JSON refused", "text/plain", 0, false, 415, "application/json"},
		{"ordinary declared 10 MiB bound", "application/json", 11 << 20, false, 413, "request body too large"},
		{"ordinary chunked 10 MiB bound", "application/json", 11 << 20, true, 400, "invalid project consolidation request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := consolidationPolicyService(t, nil)
			body := &consolidationTrackedBody{Reader: io.MultiReader(&consolidationVirtualReader{remaining: tc.size, fill: ' '}, strings.NewReader("{}"))}
			defer body.Close()
			request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/preflight", body)
			request.ContentLength = tc.size + 2
			if tc.chunked {
				request.ContentLength = -1
				request.TransferEncoding = []string{"chunked"}
			}
			request.Header.Set("Content-Type", tc.contentType)
			request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
			response := httptest.NewRecorder()
			service.router.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.message) {
				t.Fatalf("status=%d, body=%s; want %d %q", response.Code, response.Body.String(), tc.wantStatus, tc.message)
			}
			if tc.chunked && body.readBytes != (10<<20)+1 {
				t.Fatalf("ordinary streaming limit read %d bytes", body.readBytes)
			}
		})
	}
}

func TestProjectConsolidationRegisteredTimeoutAndRateCleanup(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "rate refusal", true: "deadline cleanup"}[timeout], func(t *testing.T) {
			called, released := false, false
			service := consolidationPolicyService(t, func(ctx context.Context, _, _ string, _ io.Reader) (string, error) {
				called = true
				defer func() { released = true }()
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("registered deadline did not reach the verifier")
				}
				return "", ctx.Err()
			})
			body, contentType, length := consolidationPolicyBody(t, 11<<20, [][2]string{{"plan_reference", "fixture"}, {"plan_fingerprint", "sha256:fixture"}}, false)
			request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/verify-backup", body)
			request.ContentLength = length
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
			if !timeout {
				for range 100 {
					if !service.rateLimiter.Allow(request.RemoteAddr) {
						t.Fatal("rate-limit fixture depleted before its declared burst")
					}
				}
			}
			if timeout {
				ctx, cancel := context.WithDeadline(request.Context(), time.Now().Add(-time.Second))
				defer cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			service.router.ServeHTTP(response, request)
			want := http.StatusTooManyRequests
			if timeout {
				want = http.StatusGatewayTimeout
			}
			if response.Code != want || called != timeout || released != called || !body.closed || body.readBytes > 8192 {
				t.Fatalf("status=%d, called=%v, released=%v, closed=%v, read=%d", response.Code, called, released, body.closed, body.readBytes)
			}
		})
	}
}

func TestProjectConsolidationVerifierSeamRetainsDataAuthorization(t *testing.T) {
	called := false
	service := &Service{projectConsolidationVerifierSeam: func(context.Context, string, string, io.Reader) (string, error) {
		called = true
		return "", nil
	}}
	body, contentType, _ := consolidationPolicyBody(t, 32, [][2]string{{"plan_reference", "fixture"}, {"plan_fingerprint", "sha256:fixture"}}, false)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/verify-backup", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	service.handleProjectConsolidationVerifyBackup(response, request)
	if response.Code != http.StatusForbidden || called || !body.closed || !strings.Contains(response.Body.String(), "PROJECT_ADOPTION_AUTH_REQUIRED") {
		t.Fatalf("data authorization bypassed: status=%d, called=%v, closed=%v", response.Code, called, body.closed)
	}
}

type consolidationDeadlineResponse struct {
	*httptest.ResponseRecorder
	readDeadline time.Time
}

func (response *consolidationDeadlineResponse) SetReadDeadline(deadline time.Time) error {
	response.readDeadline = deadline
	return nil
}

func TestProjectConsolidationOperationDeadline(t *testing.T) {
	for _, path := range []string{"backup", "verify-backup", "dry-run", "apply"} {
		t.Run("non-admin slow body/"+path, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			body := &consolidationTrackedBody{Reader: reader}
			request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/"+path, body)
			request = request.WithContext(auth.WithIdentity(request.Context(), auth.Client("read-write", "fixture-nonadmin")))
			response := &consolidationDeadlineResponse{ResponseRecorder: httptest.NewRecorder()}
			finished := make(chan struct{})
			handler := requestBodySize(requestTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
			})))
			go func() {
				handler.ServeHTTP(response, request)
				close(finished)
			}()
			fast := true
			select {
			case <-finished:
			case <-time.After(250 * time.Millisecond):
				fast = false
				_ = writer.Close()
				<-finished
			}
			if !fast || response.Code != http.StatusForbidden || body.maxRead != 0 || !response.readDeadline.IsZero() {
				t.Fatalf("non-admin body reached bulk lifetime: fast=%v status=%d readAttempt=%d extendedRead=%v", fast, response.Code, body.maxRead, !response.readDeadline.IsZero())
			}
		})
	}
	for _, path := range []string{"backup", "verify-backup", "dry-run", "apply", "preflight", "census"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/"+path, strings.NewReader("{}"))
			request = request.WithContext(auth.WithIdentity(request.Context(), auth.Admin()))
			response := &consolidationDeadlineResponse{ResponseRecorder: httptest.NewRecorder()}
			handler := requestBodySize(requestTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("request deadline missing")
				}
				remaining := time.Until(deadline)
				if isProjectConsolidationOperation(r) {
					if remaining <= DefaultHTTPTimeout || remaining > projectConsolidationOperationTimeout {
						t.Fatal("finite bulk operation deadline missing")
					}
				} else if remaining > DefaultHTTPTimeout {
					t.Fatal("ordinary deadline weakened")
				}
			})))
			handler.ServeHTTP(response, request)
			if path == "verify-backup" && (time.Until(response.readDeadline) <= DefaultHTTPTimeout || time.Until(response.readDeadline) > projectConsolidationOperationTimeout) {
				t.Fatal("upload read deadline did not override the server's ordinary read timeout")
			}
		})
	}
	short := time.Now().Add(time.Second)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/consolidation/verify-backup", strings.NewReader("{}"))
	ctx, cancel := context.WithDeadline(auth.WithIdentity(request.Context(), auth.Admin()), short)
	defer cancel()
	response := &consolidationDeadlineResponse{ResponseRecorder: httptest.NewRecorder()}
	requestBodySize(requestTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, _ := r.Context().Deadline()
		if !deadline.Equal(short) {
			t.Fatal("inherited deadline extended")
		}
	}))).ServeHTTP(response, request.WithContext(ctx))
	if !response.readDeadline.Equal(short) {
		t.Fatal("socket read deadline ignored inherited cancellation boundary")
	}
}

func TestProjectConsolidationUploadRealReadDeadline(t *testing.T) {
	body, contentType, length := consolidationPolicyBody(t, 32, [][2]string{{"plan_reference", "fixture"}, {"plan_fingerprint", "sha256:fixture"}}, false)
	service := consolidationPolicyService(t, func(ctx context.Context, _, _ string, source io.Reader) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= DefaultHTTPTimeout || time.Until(deadline) > projectConsolidationOperationTimeout {
			t.Fatal("registered real-server operation deadline missing")
		}
		_, err := io.Copy(io.Discard, source)
		return "fixture-backup", err
	})
	server := httptest.NewUnstartedServer(service.router)
	server.Config.ReadTimeout = 100 * time.Millisecond
	server.Config.ReadHeaderTimeout = time.Second
	server.Start()
	defer server.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, err := io.Copy(writer, body)
		_ = writer.CloseWithError(err)
	}()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/consolidation/verify-backup", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = length
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("registered upload failed with short ordinary server read timeout: %d", response.StatusCode)
	}
}

func consolidationRegisteredFixture(t *testing.T, historical bool) (*Service, *httptest.Server, *gormlib.DB, *gormlib.DB, string) {
	t.Helper()
	path, deadlineText := os.Getenv("ENGRAM_STREAM_INTEGRITY_FIXTURE"), os.Getenv("ENGRAM_STREAM_INTEGRITY_DEADLINE")
	if path == "" && deadlineText == "" {
		t.Skip("no assigned existing22417 fixture; registered store/crypto proof not run")
	}
	deadline, err := time.Parse(time.RFC3339, deadlineText)
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline) && time.Until(deadline) <= 30*time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	t.Cleanup(cancel)
	common, err := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	require.NoError(t, err)
	gitDirectory := filepath.Clean(strings.TrimSpace(string(common)))
	require.True(t, filepath.IsAbs(gitDirectory) && filepath.Base(gitDirectory) == ".git")
	root := filepath.Join(filepath.Dir(gitDirectory), ".agent", "artifacts", "data", "pr568-stream-integrity-20261017", ".private")
	absolute, err := filepath.Abs(path)
	require.NoError(t, err)
	configurationName := "oracle-fixture.json"
	if historical {
		configurationName = "fixture.json"
	}
	require.Equal(t, filepath.Join(root, configurationName), absolute)
	var fixture struct{ Lease, SourceDSN, TargetDSN, KeyPath string }
	raw, err := os.ReadFile(absolute)
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("private assigned fixture unavailable")
	}
	require.Equal(t, "r1-stream-integrity-20261017-01a0db09", fixture.Lease)
	require.Equal(t, filepath.Join(root, "vault.key"), filepath.Clean(fixture.KeyPath))
	var schema string
	if historical {
		schema = "stream_baseline6109"
	} else {
		var prepared struct{ Schema string }
		raw, err := os.ReadFile(filepath.Join(root, "registered-http-fixture.json"))
		require.NoError(t, err, "the admitted GORM fixture preparation must finish first")
		require.NoError(t, json.Unmarshal(raw, &prepared))
		require.True(t, strings.HasPrefix(prepared.Schema, "stream_http_") && len(prepared.Schema) == len("stream_http_")+32)
		schema = prepared.Schema
	}
	open := func(dsn string, source bool) (*gormlib.DB, string) {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil || cfg.Host != "127.0.0.1" || cfg.Port != 22417 || cfg.User != "stream_fixture" || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
			t.Fatal("refusing unassigned HTTP fixture")
		}
		name, oid := "engram_migration186_scope_test_stream_oracle_20261017_restore", "22327"
		if historical {
			name, oid = "engram_migration186_scope_test_stream_20261017_restore", "16390"
		}
		if source {
			name, oid = "engram_migration186_scope_test_stream_oracle_20261017_source", "22326"
			if historical {
				name, oid = "engram_migration186_scope_test_stream_20261017_source", "16389"
			}
		}
		require.Equal(t, name, cfg.Database)
		u := &url.URL{Scheme: "postgres", Host: "127.0.0.1:22417", Path: "/" + cfg.Database, User: url.UserPassword(cfg.User, cfg.Password)}
		query := url.Values{"sslmode": {"disable"}}
		for name, value := range cfg.RuntimeParams {
			query.Set(name, value)
		}
		query.Set("search_path", schema+", public")
		query.Set("row_security", "off")
		query.Set("TimeZone", "UTC")
		query.Set("statement_timeout", "60000")
		query.Set("application_name", fixture.Lease+"-http")
		if source && historical {
			query.Set("default_transaction_read_only", "on")
		}
		u.RawQuery = query.Encode()
		db, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("assigned HTTP fixture connection failed")
		}
		pool, err := db.DB()
		require.NoError(t, err)
		pool.SetMaxOpenConns(2)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		db = db.WithContext(ctx)
		var identity string
		require.NoError(t, db.Raw(`SELECT (SELECT system_identifier::text FROM pg_control_system())||'|'||oid::text||'|'||datname FROM pg_database WHERE datname=current_database()`).Scan(&identity).Error)
		require.Equal(t, "7697507119866282022|"+oid+"|"+name, identity)
		var actualSchema string
		require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
		require.Equal(t, schema, actualSchema, "never migrate or create a schema from the HTTP proof")
		return db, u.String()
	}
	source, _ := open(fixture.SourceDSN, true)
	target, targetDSN := open(fixture.TargetDSN, false)
	t.Setenv("ENGRAM_CONSOLIDATION_RESTORE_DSN", targetDSN)
	service := consolidationPolicyService(t, nil)
	service.store = &dbgorm.Store{DB: source}
	service.config = &config.Config{EncryptionKeyFile: fixture.KeyPath}
	service.credentialStore = dbgorm.NewCredentialStore(&dbgorm.Store{DB: target})
	require.Nil(t, service.projectConsolidationVerifierSeam, "this proof must use the registered real store, not a verifier seam")
	server := httptest.NewServer(service.router)
	t.Cleanup(server.Close)
	return service, server, source, target, root
}

func consolidationHTTPJSON(t *testing.T, service *Service, server *httptest.Server, method, path string, value any, status int, result any) {
	t.Helper()
	var body io.Reader
	if value != nil {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
	require.NoError(t, err)
	request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, status, response.StatusCode, "registered HTTP status; private response bodies are not logged")
	if result != nil {
		require.NoError(t, json.NewDecoder(response.Body).Decode(result))
	}
}

func consolidationHTTPVerify(t *testing.T, service *Service, server *httptest.Server, plan dbgorm.ConsolidationPlan, sealed []byte, status int) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("plan_reference", plan.Reference))
	require.NoError(t, writer.WriteField("plan_fingerprint", plan.Fingerprint))
	part, err := writer.CreateFormFile("sealed_export", "fixture.sealed")
	require.NoError(t, err)
	_, err = part.Write(sealed)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/projects/consolidation/verify-backup", &body)
	require.NoError(t, err)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, status, response.StatusCode, "real registered VerifyBackup status")
	if status != http.StatusOK {
		return ""
	}
	var result struct {
		Reference string `json:"backup_reference"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.NotEmpty(t, result.Reference)
	return result.Reference
}

func consolidationHTTPJSONValue(t *testing.T, value string) string {
	t.Helper()
	var object any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&object))
	require.ErrorIs(t, decoder.Decode(&struct{}{}), io.EOF)
	canonical, err := json.Marshal(object)
	require.NoError(t, err)
	return string(canonical)
}

func consolidationHTTPRows(t *testing.T, db *gormlib.DB) (map[string][]string, string) {
	t.Helper()
	var tables []string
	require.NoError(t, db.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') ORDER BY c.relname`).Scan(&tables).Error)
	require.NotEmpty(t, tables, "independent all-persistent-table denominator")
	rows := map[string][]string{}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, table := range tables {
		var raw []string
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		require.NoError(t, db.Raw("SELECT to_jsonb(t)::text FROM "+quoted+" t ORDER BY to_jsonb(t)::text").Scan(&raw).Error)
		rows[table] = []string{}
		for _, value := range raw {
			rows[table] = append(rows[table], consolidationHTTPJSONValue(t, value))
		}
		sort.Strings(rows[table])
		var versions []string
		require.NoError(t, db.Raw("SELECT encode(sha256(convert_to(to_jsonb(t)::text||':'||t.xmin::text,'UTF8')),'hex') FROM "+quoted+" t ORDER BY to_jsonb(t)::text,t.xmin::text").Scan(&versions).Error)
		require.NoError(t, encoder.Encode(struct {
			Table string
			Rows  []string
		}{table, versions}))
	}
	var sequences []string
	require.NoError(t, db.Raw(`SELECT sequencename FROM pg_sequences WHERE schemaname=current_schema() ORDER BY sequencename`).Scan(&sequences).Error)
	for _, name := range sequences {
		var value struct {
			LastValue int64
			IsCalled  bool
		}
		require.NoError(t, db.Raw(`SELECT last_value,is_called FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`).Scan(&value).Error)
		require.NoError(t, encoder.Encode(struct {
			Name  string
			Value any
		}{name, value}))
	}
	return rows, hex.EncodeToString(hash.Sum(nil))
}

func TestProjectConsolidationRegisteredStoreWorkflow(t *testing.T) {
	service, server, source, target, root := consolidationRegisteredFixture(t, false)
	var prepared struct {
		Source, Target struct{ Rows map[string][]string }
	}
	raw, err := os.ReadFile(filepath.Join(root, "registered-http-fixture.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &prepared))
	for _, tables := range []map[string][]string{prepared.Source.Rows, prepared.Target.Rows} {
		for table, rows := range tables {
			for i, row := range rows {
				rows[i] = consolidationHTTPJSONValue(t, row)
			}
			sort.Strings(tables[table])
		}
	}
	sourceRows, sourceBefore := consolidationHTTPRows(t, source)
	require.Equal(t, prepared.Source.Rows, sourceRows, "the exact producer source must remain unchanged")
	var census dbgorm.ConsolidationCensus
	consolidationHTTPJSON(t, service, server, http.MethodGet, "/api/projects/consolidation/census?source_version=joined-stream-integrity-fixture", nil, http.StatusOK, &census)
	require.Len(t, census.Tables, len(prepared.Source.Rows))
	vault, err := vaultcrypto.OpenExistingVault(service.config)
	require.NoError(t, err)
	censusCipher, err := hex.DecodeString(census.Reference)
	require.NoError(t, err)
	censusPlain, err := vault.Decrypt(censusCipher)
	require.NoError(t, err)
	compressed, err := gzip.NewReader(strings.NewReader(censusPlain))
	require.NoError(t, err)
	var censusInput struct {
		Kind  string
		Value struct {
			NativeSnapshot string
			Rows           []struct {
				Reference, Table, Column, Kind, SelectorReference, NativeVersion string
				Key                                                              dbgorm.JSONRaw
				Path                                                             []string
				Global                                                           bool
			}
		}
	}
	require.NoError(t, json.NewDecoder(io.LimitReader(compressed, 64<<20)).Decode(&censusInput))
	require.NoError(t, compressed.Close())
	require.Equal(t, "census", censusInput.Kind)
	var plan dbgorm.ConsolidationPlan
	consolidationHTTPJSON(t, service, server, http.MethodPost, "/api/projects/consolidation/preflight", dbgorm.ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint}, http.StatusOK, &plan)
	require.Len(t, plan.Mapping, len(censusInput.Value.Rows))
	planID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(plan.Fingerprint)).String()
	requestBody, err := json.Marshal(projectPlanRequest{plan.Reference, plan.Fingerprint})
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/projects/consolidation/backup", bytes.NewReader(requestBody))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Auth-Token", service.tokenAuth.Token())
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "application/octet-stream", response.Header.Get("Content-Type"))
	sealed, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	var frames [][]byte
	reader := bytes.NewReader(sealed)
	for reader.Len() > 0 {
		var size uint32
		require.NoError(t, binary.Read(reader, binary.BigEndian, &size))
		require.True(t, size > 0 && size <= 512<<10)
		frame := make([]byte, size)
		_, err := io.ReadFull(reader, frame)
		require.NoError(t, err)
		frames = append(frames, frame)
	}
	require.Greater(t, len(frames), 1)
	localRows, targetBefore := consolidationHTTPRows(t, target)
	controlKey := func(table, row string) string {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(row), &fields))
		require.NotEmpty(t, fields["backup_id"])
		key := string(fields["backup_id"])
		if table == "project_consolidation_frames" {
			require.NotEmpty(t, fields["ordinal"])
			key += ":" + string(fields["ordinal"])
		}
		return key
	}
	expected := make(map[string][]string, len(prepared.Source.Rows))
	controls := map[string]map[string]string{}
	for table, rows := range prepared.Source.Rows {
		expected[table] = append([]string{}, rows...)
	}
	for _, table := range []string{"project_consolidation_backups", "project_consolidation_frames"} {
		controls[table] = map[string]string{}
		for _, row := range append(append([]string{}, prepared.Source.Rows[table]...), localRows[table]...) {
			key := controlKey(table, row)
			if prior, found := controls[table][key]; found {
				require.Equal(t, prior, row, "overlapping source/local control key must retain every field")
			}
			controls[table][key] = row
		}
		for _, row := range prepared.Target.Rows[table] {
			require.Equal(t, row, controls[table][controlKey(table, row)], "producer's original local controls remain")
		}
	}
	normalize := func(table string, value any) string {
		body, err := json.Marshal(value)
		require.NoError(t, err)
		var row string
		require.NoError(t, target.Raw("SELECT to_jsonb(jsonb_populate_record(NULL::\""+table+"\",?::jsonb))::text", string(body)).Row().Scan(&row))
		return consolidationHTTPJSONValue(t, row)
	}
	consolidationHTTPVerify(t, service, server, plan, append(append([]byte{}, sealed...), 1), http.StatusConflict)
	_, refused := consolidationHTTPRows(t, target)
	require.Equal(t, targetBefore, refused, "real HTTP trailing refusal leaves every row/xmin/sequence unchanged")
	verifyStarted := time.Now().UTC()
	id := consolidationHTTPVerify(t, service, server, plan, sealed, http.StatusOK)
	verifyFinished := time.Now().UTC()
	headerHash := sha256.Sum256(frames[0])
	require.Equal(t, uuid.NewSHA1(uuid.NameSpaceOID, []byte(hex.EncodeToString(headerHash[:]))).String(), id)
	targetReader := dbgorm.NewProjectConsolidationStore(target, vault, nil)
	verifiedCensus, err := targetReader.Census(t.Context(), "registered-independent-target-read")
	require.NoError(t, err)
	var verifiedAt time.Time
	require.NoError(t, target.Raw(`SELECT restore_verified_at FROM project_consolidation_backups WHERE backup_id=?`, id).Row().Scan(&verifiedAt))
	require.False(t, verifiedAt.Before(verifyStarted.Truncate(time.Microsecond)) || verifiedAt.After(verifyFinished), "server verification timestamp belongs to this actual request")
	envelope := normalize("project_consolidation_backups", map[string]any{"backup_id": id, "plan_id": planID, "plan_fingerprint": plan.Fingerprint, "ciphertext": "\\x" + hex.EncodeToString(frames[0]), "restore_identity": verifiedCensus.Database, "restore_snapshot": verifiedCensus.Snapshot, "restore_verified_at": verifiedAt})
	require.NotContains(t, controls["project_consolidation_backups"], controlKey("project_consolidation_backups", envelope))
	controls["project_consolidation_backups"][controlKey("project_consolidation_backups", envelope)] = envelope
	for ordinal, cipher := range frames {
		row := normalize("project_consolidation_frames", map[string]any{"backup_id": id, "ordinal": ordinal, "ciphertext": "\\x" + hex.EncodeToString(cipher)})
		key := controlKey("project_consolidation_frames", row)
		require.NotContains(t, controls["project_consolidation_frames"], key)
		controls["project_consolidation_frames"][key] = row
		var stored []byte
		require.NoError(t, target.Raw(`SELECT ciphertext FROM project_consolidation_frames WHERE backup_id=? AND ordinal=?`, id, ordinal).Row().Scan(&stored))
		require.True(t, bytes.Equal(cipher, stored), "real HTTP ordered archive ciphertext conservation")
	}
	for table, keyed := range controls {
		expected[table] = []string{}
		for _, row := range keyed {
			expected[table] = append(expected[table], row)
		}
		sort.Strings(expected[table])
	}
	rows, verified := consolidationHTTPRows(t, target)
	require.Equal(t, expected, rows, "exact all-table source/local/current union; no undeclared keys, rows or ciphertext")
	require.Equal(t, id, consolidationHTTPVerify(t, service, server, plan, sealed, http.StatusOK))
	_, repeated := consolidationHTTPRows(t, target)
	require.Equal(t, verified, repeated, "real HTTP repeated Verify has no persistent writes")
	execute := dbgorm.ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()}
	var dryReceipt, receipt dbgorm.ConsolidationReceipt
	consolidationHTTPJSON(t, service, server, http.MethodPost, "/api/projects/consolidation/dry-run", execute, http.StatusOK, &dryReceipt)
	_, afterDry := consolidationHTTPRows(t, target)
	require.Equal(t, verified, afterDry)
	members := map[string]string{}
	occurrences := map[string]bool{}
	for _, row := range expected["project_consolidation_memberships"] {
		var member map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(row), &member))
		members[string(member["reference"])] = row
		occurrences[string(member["table_name"])+":"+consolidationHTTPJSONValue(t, string(member["row_key"]))+":"+consolidationHTTPJSONValue(t, string(member["field_path"]))] = true
	}
	for i, row := range censusInput.Value.Rows {
		mapping := plan.Mapping[i]
		require.Equal(t, row.Reference, mapping.Reference)
		require.Empty(t, mapping.ProjectID, "evidence-free HTTP plan must not invent a reassignment")
		if row.Global || row.Table == "project_consolidation_memberships" || row.Table == "project_consolidation_redirects" || row.Table == "project_consolidation_operations" || row.Table == "project_consolidation_backups" || row.Table == "project_consolidation_frames" {
			continue
		}
		path, err := json.Marshal(append([]string{row.Column}, row.Path...))
		require.NoError(t, err)
		table, err := json.Marshal(row.Table)
		require.NoError(t, err)
		occurrence := string(table) + ":" + consolidationHTTPJSONValue(t, string(row.Key)) + ":" + consolidationHTTPJSONValue(t, string(path))
		if occurrences[occurrence] {
			continue
		}
		member := normalize("project_consolidation_memberships", map[string]any{"reference": row.Reference, "unresolved_reference": mapping.UnresolvedReference, "table_name": row.Table, "row_key": json.RawMessage(row.Key), "source_row_key": json.RawMessage(row.Key), "field_path": json.RawMessage(path), "selector_reference": row.SelectorReference, "project_id": nil, "reason": mapping.Reason, "plan_fingerprint": plan.Fingerprint, "native_version": row.NativeVersion, "source_snapshot": censusInput.Value.NativeSnapshot})
		key, err := json.Marshal(row.Reference)
		require.NoError(t, err)
		require.NotContains(t, members, string(key))
		members[string(key)] = member
		occurrences[occurrence] = true
	}
	expected["project_consolidation_memberships"] = []string{}
	for _, member := range members {
		expected["project_consolidation_memberships"] = append(expected["project_consolidation_memberships"], member)
	}
	sort.Strings(expected["project_consolidation_memberships"])
	applyStarted := time.Now().UTC()
	consolidationHTTPJSON(t, service, server, http.MethodPost, "/api/projects/consolidation/apply", execute, http.StatusOK, &receipt)
	applyFinished := time.Now().UTC()
	require.Equal(t, planID, receipt.OperationID)
	require.Equal(t, "urn:engram:migration:receipt:"+planID, receipt.Reference)
	require.Equal(t, dryReceipt.ProjectIDs, receipt.ProjectIDs)
	require.Equal(t, dryReceipt.Roles, receipt.Roles)
	require.Equal(t, dryReceipt.RollbackBoundary, receipt.RollbackBoundary)
	appliedCensus, err := targetReader.Census(t.Context(), "registered-independent-applied-target-read")
	require.NoError(t, err)
	require.Equal(t, appliedCensus.Snapshot, receipt.Snapshot)
	var completedAt time.Time
	require.NoError(t, target.Raw(`SELECT completed_at FROM project_consolidation_operations WHERE idempotency_key=?`, execute.IdempotencyKey).Row().Scan(&completedAt))
	require.False(t, completedAt.Before(applyStarted.Truncate(time.Microsecond)) || completedAt.After(applyFinished), "server completion timestamp belongs to this actual request")
	operation := normalize("project_consolidation_operations", map[string]any{"idempotency_key": execute.IdempotencyKey, "operation_id": planID, "plan_id": planID, "plan_fingerprint": plan.Fingerprint, "receipt": receipt, "completed_at": completedAt})
	expected["project_consolidation_operations"] = append(expected["project_consolidation_operations"], operation)
	sort.Strings(expected["project_consolidation_operations"])
	appliedRows, afterApply := consolidationHTTPRows(t, target)
	require.Equal(t, expected, appliedRows, "all persisted rows equal independently declared membership/typed operation additions; no table exclusions")
	var replay dbgorm.ConsolidationReceipt
	consolidationHTTPJSON(t, service, server, http.MethodPost, "/api/projects/consolidation/apply", execute, http.StatusOK, &replay)
	require.Equal(t, receipt, replay)
	_, afterReplay := consolidationHTTPRows(t, target)
	require.Equal(t, afterApply, afterReplay)
	var credential dbgorm.Credential
	require.NoError(t, target.Where("key=?", "stream-integrity").First(&credential).Error)
	consolidationHTTPJSON(t, service, server, http.MethodGet, "/api/vault/credentials/stream-integrity?project="+url.QueryEscape(credential.Project), nil, http.StatusOK, nil)
	_, sourceAfter := consolidationHTTPRows(t, source)
	require.Equal(t, sourceBefore, sourceAfter, "registered source remains read-only")
	t.Logf("Real registered Census/Preflight/Backup/Verify/DryRun/Apply/replay: tables=%d source_rows=%d ordered_current_frames=%d exact_prior_local_backups=%d exact_prior_local_frames=%d; no verifier seam", len(expected), len(censusInput.Value.Rows), len(frames), len(localRows["project_consolidation_backups"]), len(localRows["project_consolidation_frames"]))
}

func TestProjectConsolidationRegisteredHistorical6109Reader(t *testing.T) {
	service, server, source, target, root := consolidationRegisteredFixture(t, true)
	directory := filepath.Join(root, "stream_baseline6109")
	var archive struct {
		Plan         dbgorm.ConsolidationPlan
		StreamSHA256 string
	}
	raw, err := os.ReadFile(filepath.Join(directory, "baseline6109.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &archive))
	sealed, err := os.ReadFile(filepath.Join(directory, "baseline6109.sealed"))
	require.NoError(t, err)
	digest := sha256.Sum256(sealed)
	require.Equal(t, archive.StreamSHA256, hex.EncodeToString(digest[:]))
	_, sourceBefore := consolidationHTTPRows(t, source)
	_, targetBefore := consolidationHTTPRows(t, target)
	id := consolidationHTTPVerify(t, service, server, archive.Plan, sealed, http.StatusOK)
	_, targetAfter := consolidationHTTPRows(t, target)
	require.Equal(t, targetBefore, targetAfter, "registered reader reuses the exact direct-store verified historical restoration without rewriting it")
	execute := dbgorm.ConsolidationApplyRequest{PlanReference: archive.Plan.Reference, PlanFingerprint: archive.Plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()}
	for _, operation := range []string{"dry-run", "apply"} {
		consolidationHTTPJSON(t, service, server, http.MethodPost, "/api/projects/consolidation/"+operation, execute, http.StatusConflict, nil)
	}
	vault, err := vaultcrypto.OpenExistingVault(service.config)
	require.NoError(t, err)
	var credentials []dbgorm.Credential
	require.NoError(t, target.Find(&credentials).Error)
	require.NotEmpty(t, credentials)
	for _, credential := range credentials {
		_, err := vault.Decrypt(credential.EncryptedSecret)
		require.NoError(t, err)
		consolidationHTTPJSON(t, service, server, http.MethodGet, "/api/vault/credentials/"+url.PathEscape(credential.Key)+"?project="+url.QueryEscape(credential.Project), nil, http.StatusOK, nil)
	}
	_, sourceAfter := consolidationHTTPRows(t, source)
	_, targetFinal := consolidationHTTPRows(t, target)
	require.Equal(t, sourceBefore, sourceAfter)
	require.Equal(t, targetAfter, targetFinal, "historical partial refusal and ordinary native reader have no write effects")
	retained, err := os.ReadFile(filepath.Join(directory, "baseline6109.sealed"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(sealed, retained), "genuine old ciphertext was not rewritten")
}
