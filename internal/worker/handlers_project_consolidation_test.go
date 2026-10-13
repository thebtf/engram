package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
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
}

func (body *consolidationTrackedBody) Close() error { body.closed = true; return nil }

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
				if !ok || time.Until(deadline) > DefaultHTTPTimeout {
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
