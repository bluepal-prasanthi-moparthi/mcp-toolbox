// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package arangodb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/googleapis/mcp-toolbox/internal/testutils"
	"github.com/googleapis/mcp-toolbox/tests"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	ArangoDBSourceType = "arangodb"
	ArangoDBHost       = os.Getenv("ARANGODB_HOST")
	ArangoDBPort       = os.Getenv("ARANGODB_PORT")
	ArangoDBDatabase   = os.Getenv("ARANGODB_DATABASE")
	ArangoDBUser       = os.Getenv("ARANGODB_USER")
	ArangoDBPass       = os.Getenv("ARANGODB_PASSWORD")
)

const (
	integrationTestCollection = "McpToolboxArangoIntegrationTest"
	rootPassword              = "playwithdata"
)

func arangoBaseURL(t *testing.T) string {
	t.Helper()
	switch "" {
	case ArangoDBHost:
		t.Fatal("'ARANGODB_HOST' not set")
	case ArangoDBPort:
		t.Fatal("'ARANGODB_PORT' not set")
	}
	return fmt.Sprintf("http://%s:%s", ArangoDBHost, ArangoDBPort)
}

func getArangoDBVars(t *testing.T) map[string]any {
	switch "" {
	case ArangoDBDatabase:
		t.Fatal("'ARANGODB_DATABASE' not set")
	case ArangoDBUser:
		t.Fatal("'ARANGODB_USER' not set")
	case ArangoDBPass:
		t.Fatal("'ARANGODB_PASSWORD' not set")
	}
	port := 8529
	if ArangoDBPort != "" {
		var err error
		if _, err = fmt.Sscanf(ArangoDBPort, "%d", &port); err != nil {
			t.Fatalf("invalid ARANGODB_PORT %q: %v", ArangoDBPort, err)
		}
	}
	return map[string]any{
		"type":     ArangoDBSourceType,
		"host":     ArangoDBHost,
		"port":     port,
		"database": ArangoDBDatabase,
		"user":     ArangoDBUser,
		"password": ArangoDBPass,
	}
}

func arangoRequest(t *testing.T, method, path string, body []byte, user, pass string, allowedStatus ...int) []byte {
	t.Helper()
	url := arangoBaseURL(t) + path
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.SetBasicAuth(user, pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		for _, code := range allowedStatus {
			if resp.StatusCode == code {
				return respBody
			}
		}
		t.Fatalf("request %s %s returned status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	return respBody
}

func createDatabase(t *testing.T, dbName string) {
	payload, _ := json.Marshal(map[string]string{"name": dbName})
	arangoRequest(t, http.MethodPost, "/_api/database", payload, "root", rootPassword, http.StatusConflict)
}

func seedFixtures(t *testing.T) {
	dbPath := fmt.Sprintf("/_db/%s", ArangoDBDatabase)
	arangoRequest(t, http.MethodPost, dbPath+"/_api/collection", []byte(fmt.Sprintf(`{"name":"%s"}`, integrationTestCollection)), ArangoDBUser, ArangoDBPass, http.StatusConflict)
	query := fmt.Sprintf("FOR doc IN %s REMOVE doc IN %s", integrationTestCollection, integrationTestCollection)
	runAQLHTTP(t, query, nil)
	runAQLHTTP(t, fmt.Sprintf("INSERT {name: 'Alice', status: 'active'} INTO %s", integrationTestCollection), nil)
	runAQLHTTP(t, fmt.Sprintf("INSERT {name: 'Bob', status: 'inactive'} INTO %s", integrationTestCollection), nil)
}

func teardownFixtures(t *testing.T) {
	runAQLHTTP(t, fmt.Sprintf("FOR doc IN %s REMOVE doc IN %s", integrationTestCollection, integrationTestCollection), nil)
	arangoRequest(t, http.MethodDelete, fmt.Sprintf("/_db/%s/_api/collection/%s", ArangoDBDatabase, integrationTestCollection), nil, ArangoDBUser, ArangoDBPass)
}

func runAQLHTTP(t *testing.T, query string, bindVars map[string]any) {
	t.Helper()
	reqBody := map[string]any{"query": query}
	if bindVars != nil {
		reqBody["bindVars"] = bindVars
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("failed to marshal AQL payload: %v", err)
	}
	arangoRequest(t, http.MethodPost, fmt.Sprintf("/_db/%s/_api/cursor", ArangoDBDatabase), payload, ArangoDBUser, ArangoDBPass)
}

func setupArangoDBContainer(ctx context.Context, t *testing.T) func() {
	t.Helper()
	req := testcontainers.ContainerRequest{
		Image:        "arangodb/arangodb:3.12.4",
		ExposedPorts: []string{"8529/tcp"},
		Env: map[string]string{
			"ARANGO_ROOT_PASSWORD": rootPassword,
		},
		WaitingFor: wait.ForLog("ready for business").
			WithStartupTimeout(300 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start ArangoDB container: %s", err)
	}
	cleanupFn := func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Fatalf("failed to terminate container: %s", err)
		}
	}
	host, err := container.Host(ctx)
	if err != nil {
		cleanupFn()
		t.Fatalf("failed to get container host: %s", err)
	}
	port, err := container.MappedPort(ctx, "8529")
	if err != nil {
		cleanupFn()
		t.Fatalf("failed to get mapped port: %s", err)
	}
	ArangoDBHost = host
	ArangoDBPort = port.Port()
	ArangoDBUser = "root"
	ArangoDBPass = rootPassword
	ArangoDBDatabase = "toolbox_integration"
	createDatabase(t, ArangoDBDatabase)
	return cleanupFn
}

func TestArangoDBToolEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	var containerCleanup func()
	if ArangoDBHost != "" {
		containerCleanup = func() {}
	} else {
		containerCleanup = setupArangoDBContainer(ctx, t)
	}
	t.Cleanup(containerCleanup)

	sourceConfig := getArangoDBVars(t)
	args := []string{"--enable-api"}
	toolsFile := map[string]any{
		"sources": map[string]any{
			"my-arangodb": sourceConfig,
		},
		"tools": map[string]any{
			"my-execute-aql": map[string]any{
				"type":        "arangodb-execute-aql",
				"source":      "my-arangodb",
				"description": "Execute AQL against ArangoDB.",
			},
		},
	}

	cmd, cleanup, err := tests.StartCmd(ctx, toolsFile, args...)
	if err != nil {
		t.Fatalf("command initialization returned an error: %s", err)
	}
	t.Cleanup(cleanup)

	waitCtx, cancelWait := context.WithTimeout(ctx, 30*time.Second)
	defer cancelWait()
	out, err := testutils.WaitForString(waitCtx, regexp.MustCompile(`Server ready to serve`), cmd.Out)
	if err != nil {
		t.Logf("toolbox command logs: \n%s", out)
		t.Fatalf("toolbox didn't start successfully: %s", err)
	}

	seedFixtures(t)
	t.Cleanup(func() { teardownFixtures(t) })

	manifestTcs := []struct {
		name string
		api  string
		want map[string]any
	}{
		{
			name: "get my-execute-aql",
			api:  "http://127.0.0.1:5000/api/tool/my-execute-aql/",
			want: map[string]any{
				"my-execute-aql": map[string]any{
					"description": "Execute AQL against ArangoDB.",
					"parameters": []any{
						map[string]any{
							"name":         "query",
							"type":         "string",
							"required":     true,
							"description":  "A complete AQL query to execute.",
							"authServices": []any{},
						},
						map[string]any{
							"name":                 "bindVars",
							"type":                 "object",
							"required":             false,
							"description":          "Optional AQL bind variables.",
							"authServices":         []any{},
							"additionalProperties": true,
						},
					},
					"authRequired": []any{},
				},
			},
		},
	}
	for _, tc := range manifestTcs {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(tc.api)
			if err != nil {
				t.Fatalf("error when sending a request: %s", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("response status code is not 200: got %d", resp.StatusCode)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("error parsing response body: %v", err)
			}
			got, ok := body["tools"]
			if !ok {
				t.Fatalf("unable to find tools in response body")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	invokeTcs := []struct {
		name         string
		requestBody  string
		validateFunc func(t *testing.T, body string)
	}{
		{
			name:        "simple scalar",
			requestBody: `{"query": "RETURN 1"}`,
			validateFunc: func(t *testing.T, body string) {
				if !strings.Contains(body, `"count":1`) || !strings.Contains(body, `"results":[1]`) {
					t.Fatalf("unexpected scalar response: %s", body)
				}
			},
		},
		{
			name:        "large integers",
			requestBody: `{"query": "FOR value IN [9007199254740993, {id: 9223372036854775807, nested: [-9007199254740993]}] RETURN value"}`,
			validateFunc: func(t *testing.T, body string) {
				var result struct {
					Results json.RawMessage `json:"results"`
					Count   int             `json:"count"`
				}
				if err := json.Unmarshal([]byte(body), &result); err != nil {
					t.Fatal(err)
				}
				const want = `[9007199254740993,{"id":9223372036854775807,"nested":[-9007199254740993]}]`
				if result.Count != 2 || string(result.Results) != want {
					t.Fatalf("large integers changed in response: %s", body)
				}
			},
		},
		{
			name: "parameterized read",
			requestBody: fmt.Sprintf(
				`{"query": "FOR doc IN %s FILTER doc.status == @status RETURN doc.name", "bindVars": {"status": "active"}}`,
				integrationTestCollection,
			),
			validateFunc: func(t *testing.T, body string) {
				if !strings.Contains(body, `"Alice"`) || strings.Contains(body, `"Bob"`) {
					t.Fatalf("expected only active fixture rows, got %s", body)
				}
			},
		},
	}
	for _, tc := range invokeTcs {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post("http://127.0.0.1:5000/api/tool/my-execute-aql/invoke", "application/json", bytes.NewBufferString(tc.requestBody))
			if err != nil {
				t.Fatalf("error when sending a request: %s", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(resp.Body)
				t.Fatalf("response status code: got %d: %s", resp.StatusCode, string(bodyBytes))
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("error parsing response body: %v", err)
			}
			got, ok := body["result"].(string)
			if !ok {
				t.Fatalf("unable to find result in response body: %v", body)
			}
			tc.validateFunc(t, got)
		})
	}
}
