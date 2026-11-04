package gateway

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"edu-agent-jiuwen-server/api"
	"edu-agent-jiuwen-server/internal/platform"
)

// Every registered route must be documented in api/openapi.json, and every documented operation must exist.
func TestRoutesMatchOpenAPI(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(api.OpenAPI, &spec); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for path, operations := range spec.Paths {
		for method := range operations {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	registered := map[string]bool{}
	for _, pattern := range New(platform.Config{}, nil).Patterns() {
		registered[pattern] = true
		if !documented[pattern] {
			t.Errorf("route %q is not in api/openapi.json", pattern)
		}
	}
	var missing []string
	for operation := range documented {
		if !registered[operation] {
			missing = append(missing, operation)
		}
	}
	sort.Strings(missing)
	for _, operation := range missing {
		t.Errorf("api/openapi.json documents %q but no route serves it", operation)
	}
}
