package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/docs"
	"github.com/thebtf/engram/pkg/models"
	"gopkg.in/yaml.v3"
)

func TestContextInject_PinnedUUIDDoesNotRegister(t *testing.T) {
	project := "11111111-1111-4111-8111-111111111111"
	service := newInjectTestService(true)
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, scope retrievalScope, _ int) ([]*models.Observation, error) {
		if scope.Project != project {
			t.Fatalf("retrieval project=%q", scope.Project)
		}
		return nil, nil
	}
	service.retrievalHooks.getLastPromptBySession = func(context.Context, string, string) (*models.UserPromptWithSession, error) {
		return nil, nil
	}
	service.retrievalHooks.retrieveRelevant = func(_ context.Context, selected, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		if selected != project {
			t.Fatalf("relevant project=%q", selected)
		}
		return nil, nil, nil
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"post-json-only", http.MethodPost, "/api/context/inject", `{"project":"` + project + `","session_id":"uuid-session"}`, http.StatusOK},
		{"get-query", http.MethodGet, "/api/context/inject?project=" + project, "", http.StatusOK},
		{"post-query-not-fallback", http.MethodPost, "/api/context/inject?project=" + project, `{}`, http.StatusBadRequest},
		{"get-body-ignored", http.MethodGet, "/api/context/inject", `{"project":"` + project + `"}`, http.StatusBadRequest},
		{"post-invalid-json", http.MethodPost, "/api/context/inject?project=" + project, `{`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.method == http.MethodGet {
				service.handleContextInjectGET(rec, req)
			} else {
				service.handleContextInject(rec, req)
			}
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusOK {
				return
			}
			var response struct {
				Project string `json:"project"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Project != project {
				t.Fatalf("response=%+v error=%v", response, err)
			}
		})
	}
}

func TestContextInject_RejectsRawSelectorBeforeProjectMutation(t *testing.T) {
	for _, project := range []string{"", "prc-http-no-store", "../project", "invalid selector"} {
		payload, err := json.Marshal(map[string]string{"project": project})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		(&Service{}).handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("project=%q status=%d body=%s", project, rec.Code, rec.Body.String())
		}
	}
}

func TestContextSchema_MethodSpecificRequests(t *testing.T) {
	var served, published map[string]any
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &served); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../docs/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &published); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"paths", "definitions", "securityDefinitions"} {
		if !reflect.DeepEqual(served[field], published[field]) {
			t.Fatalf("served docs.go and published swagger.json differ in %s", field)
		}
	}
	data, err = os.ReadFile("../../docs/swagger.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var yamlDoc map[string]any
	if err := yaml.Unmarshal(data, &yamlDoc); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(yamlDoc)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(published, normalized) {
		t.Fatal("swagger.json and swagger.yaml differ")
	}
	paths := served["paths"].(map[string]any)
	for _, path := range []string{"/api/context/inject", "/api/context/search", "/api/context/session-start"} {
		for _, method := range []string{"get", "post"} {
			t.Run(method+path, func(t *testing.T) {
				op := paths[path].(map[string]any)[method].(map[string]any)
				params := map[string]map[string]any{}
				for _, raw := range op["parameters"].([]any) {
					param := raw.(map[string]any)
					params[param["in"].(string)+"/"+param["name"].(string)] = param
				}
				if method == "get" {
					if params["query/project"]["required"] != true {
						t.Error("GET must require query project")
					}
					if params["body/body"] != nil {
						t.Error("GET must not advertise an ignored JSON body")
					}
					if path == "/api/context/search" && params["query/query"]["required"] != true {
						t.Error("GET search must require query")
					}
				} else {
					if params["query/project"]["required"] == true {
						t.Error("valid JSON-only POST must not require query project")
					}
					body := params["body/body"]
					if body == nil {
						t.Fatal("POST JSON body is undocumented")
					}
					if path == "/api/context/inject" {
						if params["query/project"] != nil || body["required"] != true {
							t.Error("POST inject requires JSON, not query project")
						}
						ref, _ := body["schema"].(map[string]any)["$ref"].(string)
						if ref != "#/definitions/worker.contextInjectRequest" {
							t.Fatalf("POST inject must describe its real DTO, got %q", ref)
						}
						definition := served["definitions"].(map[string]any)["worker.contextInjectRequest"].(map[string]any)
						if !reflect.DeepEqual(definition["required"], []any{"project"}) {
							t.Error("only project is required in POST inject JSON")
						}
					} else if path == "/api/context/search" && body["required"] == true {
						t.Error("POST search must preserve query-only requests")
					} else if path == "/api/context/session-start" && body["required"] != true {
						t.Error("POST session-start requires valid JSON, but project can come from the query")
					}
				}
				security, _ := op["security"].([]any)
				if len(security) != 1 || security[0].(map[string]any)["ApiKeyAuth"] == nil {
					t.Error("ApiKeyAuth must remain documented for every operation")
				}
			})
		}
	}
}

func TestContextRequests_POSTProjectFallback(t *testing.T) {
	const project = "11111111-1111-4111-8111-111111111111"
	for _, body := range []string{`{}`, `{"project":"` + project + `","query":"prompt"}`} {
		t.Run(body, func(t *testing.T) {
			service := newInjectTestService(true)
			service.retrievalHooks.retrieveRelevant = func(_ context.Context, selected, query string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
				if selected != project || query != "prompt" {
					t.Fatalf("search project=%q query=%q", selected, query)
				}
				return []*models.Observation{{ID: 1}}, nil, nil
			}
			provider := &stubSessionStartContextServer{}
			service.grpcInternalServer = provider
			for _, endpoint := range []string{"search", "session-start"} {
				path := "/api/context/" + endpoint
				if body == `{}` {
					path += "?project=" + project + "&query=prompt"
				}
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				writer := httptest.NewRecorder()
				if endpoint == "search" {
					service.handleSearchByPrompt(writer, request)
				} else {
					service.handleSessionStartContextStatic(writer, request)
					if provider.req == nil || provider.req.Project != project {
						t.Fatalf("session-start request=%v", provider.req)
					}
				}
				if writer.Code != http.StatusOK {
					t.Fatalf("%s status=%d body=%s", endpoint, writer.Code, writer.Body.String())
				}
			}
		})
	}
}

func TestContextRequests_POSTInvalidBodyFallback(t *testing.T) {
	const query = "?project=11111111-1111-4111-8111-111111111111&query=prompt"
	for _, body := range []string{"", "{"} {
		service := newInjectTestService(true)
		service.retrievalHooks.retrieveRelevant = func(context.Context, string, string, RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
			return []*models.Observation{{ID: 1}}, nil, nil
		}
		writer := httptest.NewRecorder()
		service.handleSearchByPrompt(writer, httptest.NewRequest(http.MethodPost, "/api/context/search"+query, strings.NewReader(body)))
		if writer.Code != http.StatusOK {
			t.Fatalf("search body=%q status=%d", body, writer.Code)
		}
		writer = httptest.NewRecorder()
		service.handleSessionStartContextStatic(writer, httptest.NewRequest(http.MethodPost, "/api/context/session-start"+query, strings.NewReader(body)))
		if writer.Code != http.StatusBadRequest || !strings.Contains(writer.Body.String(), "Invalid JSON") {
			t.Fatalf("session-start body=%q status=%d response=%s", body, writer.Code, writer.Body.String())
		}
	}
}
