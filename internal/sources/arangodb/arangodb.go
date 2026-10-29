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

// Package arangodb implements the ArangoDB source.
package arangodb

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	adb "github.com/arangodb/go-driver/v2/arangodb"
	"github.com/arangodb/go-driver/v2/connection"
	"github.com/goccy/go-yaml"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"go.opentelemetry.io/otel/trace"
)

const SourceType = "arangodb"

var _ sources.SourceConfig = Config{}

func init() {
	if !sources.Register(SourceType, newConfig) {
		panic(fmt.Sprintf("source type %q already registered", SourceType))
	}
}

func newConfig(ctx context.Context, name string, decoder *yaml.Decoder) (sources.SourceConfig, error) {
	actual := Config{Name: name}
	if err := decoder.DecodeContext(ctx, &actual); err != nil {
		return nil, err
	}
	return actual, nil
}

// Config is deliberately limited to a single plain HTTP ArangoDB endpoint.
type Config struct {
	Name     string `yaml:"name" validate:"required"`
	Type     string `yaml:"type" validate:"required"`
	Host     string `yaml:"host" validate:"required"`
	Port     int    `yaml:"port" validate:"required,min=1,max=65535"`
	Database string `yaml:"database" validate:"required"`
	User     string `yaml:"user" validate:"required"`
	Password string `yaml:"password" validate:"required"`
}

func (c Config) SourceConfigType() string { return SourceType }

// Endpoint returns the source's fixed, non-TLS HTTP endpoint.
func (c Config) Endpoint() (string, error) {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return "", fmt.Errorf("ArangoDB host is required")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") {
		return "", fmt.Errorf("ArangoDB host must not include a scheme or path")
	}
	if c.Port < 1 || c.Port > 65535 {
		return "", fmt.Errorf("ArangoDB port must be between 1 and 65535")
	}
	endpoint := "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", c.Port))
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid ArangoDB endpoint")
	}
	return endpoint, nil
}

func (c Config) validate() error {
	if _, err := c.Endpoint(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Database) == "" {
		return fmt.Errorf("ArangoDB database is required")
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("ArangoDB user is required")
	}
	if c.Password == "" {
		return fmt.Errorf("ArangoDB password is required")
	}
	return nil
}

type database interface {
	Query(context.Context, string, *adb.QueryOptions) (adb.Cursor, error)
}

type client interface {
	GetDatabase(context.Context, string, *adb.GetDatabaseOptions) (database, error)
}

type driverClient struct{ adb.Client }

func (c driverClient) GetDatabase(ctx context.Context, name string, options *adb.GetDatabaseOptions) (database, error) {
	return c.Client.GetDatabase(ctx, name, options)
}

var newClient = func(endpoint, user, password string) (client, error) {
	conn := connection.NewHttpConnection(connection.HttpConfiguration{
		Endpoint:       connection.NewRoundRobinEndpoints([]string{endpoint}),
		Authentication: connection.NewBasicAuth(user, password),
	})
	return driverClient{adb.NewClient(conn)}, nil
}

// Initialize opens the configured database. GetDatabase performs a request,
// which verifies endpoint reachability, authentication, and database existence.
func (c Config) Initialize(ctx context.Context, tracer trace.Tracer) (sources.Source, error) {
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid ArangoDB configuration: %w", err)
	}
	endpoint, _ := c.Endpoint()
	ctx, span := sources.InitConnectionSpan(ctx, tracer, SourceType, c.Name)
	defer span.End()
	cl, err := newClient(endpoint, c.User, c.Password)
	if err != nil {
		return nil, fmt.Errorf("unable to create ArangoDB client: %w", err)
	}
	db, err := cl.GetDatabase(ctx, c.Database, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to ArangoDB database %q: %w", c.Database, err)
	}
	return &Source{Config: c, Database: db}, nil
}

var _ sources.Source = &Source{}

// Source exposes only AQL execution to native tools. The HTTP driver has no
// client-wide shutdown operation; cursors are closed for every query instead.
type Source struct {
	Config
	Database database
}

func (s *Source) SourceType() string { return SourceType }

func (s *Source) IsReadOnly() bool { return false }

func (s *Source) ToConfig() sources.SourceConfig { return s.Config }

// ArangoDBSource marks this source as compatible with ArangoDB-native tools.
func (s *Source) ArangoDBSource() {}

// RunAQL executes a dynamic AQL query and returns JSON-compatible rows.
func (s *Source) RunAQL(ctx context.Context, query string, bindVars map[string]any) ([]any, error) {
	cursor, err := s.Database.Query(ctx, query, &adb.QueryOptions{BindVars: bindVars})
	if err != nil {
		return nil, fmt.Errorf("unable to execute AQL query: %w", err)
	}
	defer func() {
		// A canceled request must still release its server-side cursor. Bound
		// cleanup independently and preserve the original query/read error.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = cursor.CloseWithContext(cleanupCtx)
	}()

	results := make([]any, 0)
	for cursor.HasMore() {
		// Keep the original JSON so large integers (including nested values)
		// are not rounded through float64 when the response is serialized.
		var row json.RawMessage
		if _, err := cursor.ReadDocument(ctx, &row); err != nil {
			return nil, fmt.Errorf("unable to read AQL cursor: %w", err)
		}
		results = append(results, row)
	}
	return results, nil
}
