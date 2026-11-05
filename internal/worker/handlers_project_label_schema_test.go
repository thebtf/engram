package worker

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/docs"
	"gopkg.in/yaml.v3"
)

func TestProjectLabelSchemaParity(t *testing.T) {
	jsonBytes, err := os.ReadFile("../../docs/swagger.json")
	require.NoError(t, err)
	yamlBytes, err := os.ReadFile("../../docs/swagger.yaml")
	require.NoError(t, err)
	var fromYAML map[string]any
	require.NoError(t, yaml.Unmarshal(yamlBytes, &fromYAML))
	normalizedYAML, err := json.Marshal(fromYAML)
	require.NoError(t, err)

	var reference map[string]any
	for _, source := range []struct {
		name string
		data []byte
	}{
		{name: "served", data: []byte(docs.SwaggerInfo.ReadDoc())},
		{name: "json", data: jsonBytes},
		{name: "yaml", data: normalizedYAML},
	} {
		t.Run(source.name, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, json.Unmarshal(source.data, &doc))
			paths := doc["paths"].(map[string]any)
			definitions := doc["definitions"].(map[string]any)
			if reference == nil {
				reference = map[string]any{"paths": paths, "definitions": definitions, "securityDefinitions": doc["securityDefinitions"]}
			} else {
				require.Equal(t, reference["paths"], paths)
				require.Equal(t, reference["definitions"], definitions)
				require.Equal(t, reference["securityDefinitions"], doc["securityDefinitions"])
			}
			projectPath := paths["/api/projects/{id}"].(map[string]any)
			patch := projectPath["patch"].(map[string]any)
			security := patch["security"].([]any)
			require.Len(t, security, 1)
			require.Contains(t, security[0].(map[string]any), "ApiKeyAuth")
			var body map[string]any
			for _, raw := range patch["parameters"].([]any) {
				param := raw.(map[string]any)
				if param["in"] == "body" {
					body = param
				}
			}
			require.NotNil(t, body)
			require.Equal(t, true, body["required"])
			require.Equal(t, "#/definitions/worker.projectLabelPatchSchema", body["schema"].(map[string]any)["$ref"])
			request := definitions["worker.projectLabelPatchSchema"].(map[string]any)
			require.ElementsMatch(t, []any{"display_name", "expected_display_name"}, request["required"].([]any))
			properties := request["properties"].(map[string]any)
			for _, name := range []string{"display_name", "expected_display_name"} {
				property := properties[name].(map[string]any)
				require.Equal(t, "string", property["type"])
				require.Equal(t, true, property["x-nullable"])
			}
			get := paths["/api/projects"].(map[string]any)["get"].(map[string]any)
			response := get["responses"].(map[string]any)["200"].(map[string]any)
			require.Equal(t, "#/definitions/worker.projectListResponse", response["schema"].(map[string]any)["$ref"])
			catalog := definitions["worker.projectListResponse"].(map[string]any)
			items := catalog["properties"].(map[string]any)["items"].(map[string]any)
			require.Equal(t, "#/definitions/gorm.ProjectDescriptor", items["items"].(map[string]any)["$ref"])
			descriptor := definitions["gorm.ProjectDescriptor"].(map[string]any)
			require.Contains(t, descriptor["required"].([]any), "display_name")
			stored := descriptor["properties"].(map[string]any)["display_name"].(map[string]any)
			require.Equal(t, "string", stored["type"])
			require.Equal(t, true, stored["x-nullable"])
			require.Contains(t, descriptor["properties"].(map[string]any), "name")
		})
	}
}
