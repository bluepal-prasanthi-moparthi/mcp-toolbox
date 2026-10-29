package arangodbexecuteaql

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/tools"
	"github.com/googleapis/mcp-toolbox/internal/util"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
)

type mockSource struct {
	query string
	bind  map[string]any
	rows  []any
	err   error
}

func (s *mockSource) SourceType() string             { return "arangodb" }
func (s *mockSource) ToConfig() sources.SourceConfig { return nil }
func (s *mockSource) IsReadOnly() bool               { return false }
func (s *mockSource) ArangoDBSource()                {}
func (s *mockSource) RunAQL(_ context.Context, query string, bind map[string]any) ([]any, error) {
	s.query, s.bind = query, bind
	return s.rows, s.err
}

func newTool(t *testing.T) Tool {
	t.Helper()
	tool, err := (Config{ConfigBase: tools.ConfigBase{Name: "execute", Description: "Execute AQL"}, Type: resourceType, Source: "db"}).Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tool.(Tool)
}

func invoke(t *testing.T, tool Tool, source sources.Source, values parameters.ParamValues) (any, util.ToolboxError) {
	t.Helper()
	return tool.Invoke(context.Background(), source, values, tools.AccessToken(""))
}

func TestInvoke(t *testing.T) {
	tool := newTool(t)
	tests := []struct {
		name   string
		values parameters.ParamValues
		rows   []any
		want   map[string]any
		query  string
		bind   map[string]any
	}{
		{"objects", parameters.ParamValues{{Name: "query", Value: "FOR x IN docs RETURN x"}, {Name: "bindVars", Value: map[string]any{"status": "active"}}}, []any{map[string]any{"name": "toolbox"}, map[string]any{"name": "arangodb"}}, map[string]any{"results": []any{map[string]any{"name": "toolbox"}, map[string]any{"name": "arangodb"}}, "count": 2}, "FOR x IN docs RETURN x", map[string]any{"status": "active"}},
		{"scalars", parameters.ParamValues{{Name: "query", Value: "RETURN 1"}, {Name: "bindVars", Value: nil}}, []any{1, "two", nil}, map[string]any{"results": []any{1, "two", nil}, "count": 3}, "RETURN 1", nil},
		{"empty", parameters.ParamValues{{Name: "query", Value: "RETURN []"}, {Name: "bindVars", Value: nil}}, []any{}, map[string]any{"results": []any{}, "count": 0}, "RETURN []", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := &mockSource{rows: tc.rows}
			got, err := invoke(t, tool, source, tc.values)
			diff := cmp.Diff(tc.want, got)
			if err != nil || diff != "" {
				t.Fatalf("Invoke() error=%v diff=%s", err, diff)
			}
			diff = cmp.Diff(tc.bind, source.bind)
			if source.query != tc.query || diff != "" {
				t.Fatalf("query/bind vars changed: query=%q diff=%s", source.query, diff)
			}
		})
	}
}

func TestInvokeValidationAndErrors(t *testing.T) {
	tool := newTool(t)
	valid := parameters.ParamValues{{Name: "query", Value: "RETURN 1"}, {Name: "bindVars", Value: nil}}
	for _, tc := range []struct {
		name    string
		source  sources.Source
		values  parameters.ParamValues
		wantCat string
	}{
		{"incompatible source", incompatibleSource{}, valid, "SERVER_ERROR"},
		{"missing query", &mockSource{}, parameters.ParamValues{{Name: "query", Value: nil}}, "AGENT_ERROR"},
		{"empty query", &mockSource{}, parameters.ParamValues{{Name: "query", Value: "  "}}, "AGENT_ERROR"},
		{"invalid bind vars", &mockSource{}, parameters.ParamValues{{Name: "query", Value: "RETURN 1"}, {Name: "bindVars", Value: "no"}}, "AGENT_ERROR"},
		{"aql error", &mockSource{err: errors.New("AQL parse error")}, valid, "AGENT_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := invoke(t, tool, tc.source, tc.values)
			if err == nil || string(err.Category()) != tc.wantCat {
				t.Fatalf("Invoke() error=%v category=%v", err, err.Category())
			}
		})
	}
}

type incompatibleSource struct{}

func (incompatibleSource) SourceType() string             { return "other" }
func (incompatibleSource) ToConfig() sources.SourceConfig { return nil }
func (incompatibleSource) IsReadOnly() bool               { return false }

func TestRegistrationAndManifest(t *testing.T) {
	tool := newTool(t)
	manifest := tool.StaticManifest()
	if len(manifest.Parameters) != 2 || manifest.Parameters[0].Name != "query" || manifest.Parameters[1].Type != "object" || manifest.Parameters[1].Required {
		t.Fatalf("unexpected manifest: %#v", manifest.Parameters)
	}
	annotations := tool.GetAnnotations(nil)
	if annotations.ReadOnlyHint == nil || *annotations.ReadOnlyHint || annotations.IdempotentHint != nil {
		t.Fatal("arbitrary AQL must not be marked read-only or idempotent")
	}
}
