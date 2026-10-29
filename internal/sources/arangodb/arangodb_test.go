package arangodb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adb "github.com/arangodb/go-driver/v2/arangodb"
	"github.com/arangodb/go-driver/v2/connection"
	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/mcp-toolbox/internal/server"
	"github.com/googleapis/mcp-toolbox/internal/testutils"
	"go.opentelemetry.io/otel/trace/noop"
)

type mockDatabase struct{ err error }

func (d mockDatabase) Query(context.Context, string, *adb.QueryOptions) (adb.Cursor, error) {
	return nil, d.err
}

type mockCursor struct {
	rows    []any
	pos     int
	readErr error
	closed  bool
	onRead  func()
	onClose func(context.Context) error
}

func (c *mockCursor) Close() error { c.closed = true; return nil }
func (c *mockCursor) CloseWithContext(ctx context.Context) error {
	c.closed = true
	if c.onClose != nil {
		return c.onClose(ctx)
	}
	return nil
}
func (c *mockCursor) HasMore() bool { return c.pos < len(c.rows) }
func (c *mockCursor) ReadDocument(_ context.Context, result interface{}) (adb.DocumentMeta, error) {
	if c.onRead != nil {
		c.onRead()
	}
	if c.readErr != nil {
		return adb.DocumentMeta{}, c.readErr
	}
	data, err := json.Marshal(c.rows[c.pos])
	if err != nil {
		return adb.DocumentMeta{}, err
	}
	if err := json.Unmarshal(data, result); err != nil {
		return adb.DocumentMeta{}, err
	}
	c.pos++
	return adb.DocumentMeta{}, nil
}
func (c *mockCursor) Count() int64                { return int64(len(c.rows)) }
func (c *mockCursor) Statistics() adb.CursorStats { return adb.CursorStats{} }
func (c *mockCursor) Plan() adb.CursorPlan        { return adb.CursorPlan{} }

type cursorDatabase struct{ cursor adb.Cursor }

func (d cursorDatabase) Query(context.Context, string, *adb.QueryOptions) (adb.Cursor, error) {
	return d.cursor, nil
}

type mockClient struct {
	db  database
	err error
}

func (c mockClient) GetDatabase(context.Context, string, *adb.GetDatabaseOptions) (database, error) {
	return c.db, c.err
}

func TestConfigEndpoint(t *testing.T) {
	endpoint, err := (Config{Host: "127.0.0.1", Port: 8529}).Endpoint()
	if err != nil || endpoint != "http://127.0.0.1:8529" {
		t.Fatalf("Endpoint() = %q, %v", endpoint, err)
	}
	for _, cfg := range []Config{{Host: "", Port: 8529}, {Host: "http://localhost", Port: 8529}, {Host: "localhost", Port: 0}, {Host: "localhost", Port: 65536}} {
		if _, err := cfg.Endpoint(); err == nil {
			t.Fatalf("Endpoint(%+v) succeeded", cfg)
		}
	}
}

func TestParseFromYAMLAndValidation(t *testing.T) {
	valid := `kind: source
name: my-arangodb
type: arangodb
host: 127.0.0.1
port: 8529
database: toolbox
user: root
password: ${ARANGODB_PASSWORD}`
	t.Setenv("ARANGODB_PASSWORD", "secret")
	got, _, _, _, _, _, _, _, err := server.UnmarshalPrimitiveConfig(context.Background(), []byte(valid))
	if err != nil {
		t.Fatalf("UnmarshalPrimitiveConfig() error = %v", err)
	}
	want := server.SourceConfigs{"my-arangodb": Config{Name: "my-arangodb", Type: SourceType, Host: "127.0.0.1", Port: 8529, Database: "toolbox", User: "root", Password: "${ARANGODB_PASSWORD}"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("config mismatch (-want +got):\n%s", diff)
	}
	for _, cfg := range []Config{
		{Host: "", Port: 8529, Database: "db", User: "user", Password: "pass"},
		{Host: "localhost", Port: 0, Database: "db", User: "user", Password: "pass"},
		{Host: "localhost", Port: 8529, Database: "", User: "user", Password: "pass"},
		{Host: "localhost", Port: 8529, Database: "db", User: "", Password: "pass"},
		{Host: "localhost", Port: 8529, Database: "db", User: "user", Password: ""},
	} {
		if err := cfg.validate(); err == nil {
			t.Errorf("invalid config unexpectedly passed validation: %+v", cfg)
		}
	}
}

func TestInitialize(t *testing.T) {
	original := newClient
	t.Cleanup(func() { newClient = original })
	cfg := Config{Name: "test", Type: SourceType, Host: "localhost", Port: 8529, Database: "db", User: "user", Password: "not-for-errors"}
	tracer := noop.NewTracerProvider().Tracer("test")
	newClient = func(endpoint, user, password string) (client, error) {
		if endpoint != "http://localhost:8529" || user != "user" || password != "not-for-errors" {
			t.Fatal("unexpected client parameters")
		}
		return mockClient{db: mockDatabase{}}, nil
	}
	if _, err := cfg.Initialize(context.Background(), tracer); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"authentication", errors.New("status 401")},
		{"database unavailable", errors.New("status 404")},
		{"connection", errors.New("connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newClient = func(string, string, string) (client, error) { return mockClient{err: tc.err}, nil }
			_, err := cfg.Initialize(context.Background(), tracer)
			if err == nil || strings.Contains(err.Error(), cfg.Password) {
				t.Fatalf("Initialize() error = %v; credentials must not be exposed", err)
			}
		})
	}
}

func TestRegistration(t *testing.T) {
	_, _, _, _, _, _, _, _, err := server.UnmarshalPrimitiveConfig(context.Background(), testutils.FormatYaml(`
kind: source
name: registered
type: arangodb
host: localhost
port: 8529
database: db
user: user
password: pass`))
	if err != nil {
		t.Fatalf("registered source could not be decoded: %v", err)
	}
}

func TestRunAQLReadsAllRowsAndClosesCursor(t *testing.T) {
	cursor := &mockCursor{rows: []any{map[string]any{"name": "toolbox"}, 1, nil}}
	source := &Source{Database: cursorDatabase{cursor: cursor}}
	got, err := source.RunAQL(context.Background(), "RETURN 1", map[string]any{"value": 1})
	diff := cmp.Diff([]any{json.RawMessage(`{"name":"toolbox"}`), json.RawMessage(`1`), json.RawMessage(`null`)}, got)
	if err != nil || !cursor.closed || diff != "" {
		t.Fatalf("RunAQL() err=%v closed=%v diff=%s", err, cursor.closed, diff)
	}
	cursor = &mockCursor{rows: []any{1}, readErr: errors.New("cursor failed")}
	_, err = (&Source{Database: cursorDatabase{cursor: cursor}}).RunAQL(context.Background(), "RETURN 1", nil)
	if err == nil || !cursor.closed {
		t.Fatalf("RunAQL() error=%v closed=%v", err, cursor.closed)
	}
}

func TestRunAQLPreservesJSONNumbers(t *testing.T) {
	// Exercise the real driver's decoding, including numbers nested in rows.
	const rows = `[9007199254740993,{"id":9223372036854775807,"values":[-9007199254740993,1.25]},null,"text",true]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/_db/test/_api/cursor" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"result":` + rows + `,"hasMore":false,"error":false,"code":201}`))
	}))
	t.Cleanup(server.Close)
	conn := connection.NewHttpConnection(connection.HttpConfiguration{
		Endpoint: connection.NewRoundRobinEndpoints([]string{server.URL}),
	})
	db, err := adb.NewClient(conn).GetDatabase(context.Background(), "test", &adb.GetDatabaseOptions{SkipExistCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Source{Database: db}).RunAQL(context.Background(), "RETURN 9007199254740993", nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != rows {
		t.Fatalf("serialized rows = %s, want %s", encoded, rows)
	}
}

func TestRunAQLCursorContinuation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failNext bool
	}{
		{name: "reads all batches"},
		{name: "cleans up after continuation failure", failNext: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const cursorPath = "/_db/test/_api/cursor"
			requests := make(chan string, 16)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := r.Method + " " + r.URL.Path
				select {
				case requests <- request:
				default:
					t.Error("too many cursor requests")
					http.Error(w, "too many requests", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch request {
				case "POST " + cursorPath:
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"123","result":[1,{"name":"first batch"}],"hasMore":true,"error":false,"code":201}`))
				case "POST " + cursorPath + "/123":
					if tc.failNext {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":true,"code":400,"errorNum":10,"errorMessage":"continuation failed"}`))
						return
					}
					// Exhausting the cursor releases it on the server; the last
					// batch has no ID and needs no explicit DELETE.
					_, _ = w.Write([]byte(`{"result":[9007199254740993,null],"hasMore":false,"error":false,"code":200}`))
				case "DELETE " + cursorPath + "/123":
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"error":false,"code":202}`))
				default:
					t.Errorf("unexpected request: %s", request)
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			conn := connection.NewHttpConnection(connection.HttpConfiguration{
				Endpoint: connection.NewRoundRobinEndpoints([]string{server.URL}),
			})
			db, err := adb.NewClient(conn).GetDatabase(ctx, "test", &adb.GetDatabaseOptions{SkipExistCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := (&Source{Database: db}).RunAQL(ctx, "FOR value IN [1, 2, 3, 4] RETURN value", nil)
			if tc.failNext {
				if err == nil || !strings.Contains(err.Error(), "unable to read AQL cursor") || got != nil {
					t.Fatalf("RunAQL() = %v, %v; want read error and no partial results", got, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if want := `[1,{"name":"first batch"},9007199254740993,null]`; string(encoded) != want {
					t.Fatalf("serialized rows = %s, want %s", encoded, want)
				}
			}
			wantRequests := []string{"POST " + cursorPath, "POST " + cursorPath + "/123"}
			if tc.failNext {
				wantRequests = append(wantRequests, "DELETE "+cursorPath+"/123")
			}
			var gotRequests []string
			for len(requests) > 0 {
				gotRequests = append(gotRequests, <-requests)
			}
			if diff := cmp.Diff(wantRequests, gotRequests); diff != "" {
				t.Fatalf("cursor requests mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunAQLClosesCursorAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var cleanupCtx context.Context
	cursor := &mockCursor{
		rows:    []any{1},
		onRead:  cancel,
		readErr: context.Canceled,
		onClose: func(ctx context.Context) error {
			cleanupCtx = ctx
			if err := ctx.Err(); err != nil {
				t.Errorf("cleanup context is already canceled: %v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
				t.Errorf("cleanup needs a bounded future deadline, got %v (set=%v)", deadline, ok)
			}
			return errors.New("cleanup failed")
		},
	}
	_, err := (&Source{Database: cursorDatabase{cursor: cursor}}).RunAQL(ctx, "RETURN 1", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAQL() error = %v, want original cancellation error", err)
	}
	if !cursor.closed || cleanupCtx == nil {
		t.Fatal("cursor cleanup was not called")
	}
	if !errors.Is(cleanupCtx.Err(), context.Canceled) {
		t.Fatal("cleanup context was not canceled after use")
	}
}
