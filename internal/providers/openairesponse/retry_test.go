package openairesponse

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"host-scout/internal/model"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func retryClient(server *httptest.Server) *Client {
	return &Client{BaseURL: server.URL + "/v1", Model: "test", APIKey: "secret"}
}

func writeComplete(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "data: "+completedText+"\n\n")
}

func TestRetryUsesSameRequestAndStopsAfterThirdAttempt(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts int
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if r.URL.Path != "/v1/responses" {
					t.Error("wrong endpoint")
				}
				current, _ := io.ReadAll(r.Body)
				if attempts == 1 {
					body = current
				} else if string(body) != string(current) {
					t.Error("request snapshot changed")
				}
				if attempts == 1 {
					w.WriteHeader(status)
					return
				}
				writeComplete(w)
			}))
			defer server.Close()
			client := retryClient(server)
			var waits []time.Duration
			client.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
			result, err := client.Create(context.Background(), model.Request{Items: []model.Item{{Kind: model.User, Text: "question"}}}, nil)
			if err != nil || result.Text != "answer" || attempts != 2 || len(waits) != 1 || waits[0] != 500*time.Millisecond {
				t.Fatalf("%+v %v, attempts=%d waits=%v", result, err, attempts, waits)
			}
		})
	}
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts++; w.WriteHeader(503) }))
	defer server.Close()
	client := retryClient(server)
	var waits []time.Duration
	client.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	_, err := client.Create(context.Background(), model.Request{}, nil)
	assertKind(t, err, model.HTTP)
	if attempts != 3 || len(waits) != 2 || waits[0] != 500*time.Millisecond || waits[1] != time.Second {
		t.Fatalf("attempts=%d waits=%v", attempts, waits)
	}
}

func TestRetryAfterAndPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		header       string
		wantWait     time.Duration
		wantAttempts int
	}{
		{"server delay", 429, "2", 2 * time.Second, 3},
		{"too long", 429, "60", 0, 1},
		{"auth", 401, "", 0, 1},
		{"bad request", 400, "", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client := retryClient(server)
			var waited time.Duration
			client.sleep = func(_ context.Context, d time.Duration) error { waited = d; return nil }
			_, err := client.Create(context.Background(), model.Request{}, nil)
			assertKind(t, err, model.HTTP)
			if attempts != tc.wantAttempts || waited != tc.wantWait {
				t.Fatalf("attempts=%d waited=%v", attempts, waited)
			}
		})
	}
	client := &Client{BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: failingTransport{err: x509.UnknownAuthorityError{}}}}
	client.sleep = func(context.Context, time.Duration) error { t.Fatal("permanent failure retried"); return nil }
	_, err := client.Create(context.Background(), model.Request{}, nil)
	assertKind(t, err, model.Transport)
	dateClient := &Client{now: func() time.Time { return time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) }}
	if d, ok := dateClient.retryAfter("Sun, 11 Oct 2026 00:00:05 GMT"); !ok || d != 5*time.Second {
		t.Fatalf("date Retry-After: %v %v", d, ok)
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestRetryBeforeDataAndNeverAfterData(t *testing.T) {
	for _, partial := range []string{"", `{"type":"response.created"}`, `{"type":"response.function_call_arguments.delta","delta":"{}"}`, `{"type":"response.output_text.delta","delta":"partial"}`} {
		t.Run(partial, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				if attempts > 1 {
					writeComplete(w)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Length", "1000")
				if partial != "" {
					_, _ = io.WriteString(w, "data: "+partial+"\n\n")
				} else {
					w.WriteHeader(200)
				}
			}))
			defer server.Close()
			client := retryClient(server)
			client.sleep = func(context.Context, time.Duration) error { return nil }
			var visible string
			result, err := client.Create(context.Background(), model.Request{}, func(s string) { visible += s })
			if partial == "" {
				if err != nil || result.Text != "answer" || attempts != 2 {
					t.Fatalf("before data: %+v %v attempts=%d", result, err, attempts)
				}
			} else if err == nil || attempts != 1 || len(result.Items) != 0 || len(result.Calls) != 0 || (strings.Contains(partial, "output_text") && visible != "partial") {
				t.Fatalf("after data: %+v %v attempts=%d", result, err, attempts)
			}
		})
	}
}

func TestRetryWaitCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client := retryClient(server)
	client.sleep = func(ctx context.Context, _ time.Duration) error { cancel(); <-ctx.Done(); return ctx.Err() }
	_, err := client.Create(ctx, model.Request{}, nil)
	assertKind(t, err, model.Cancelled)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("cancel error: %v", err)
	}
}

func TestRetrySharesOneCallDeadline(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts++; w.WriteHeader(503) }))
	defer server.Close()
	client := retryClient(server)
	client.callTimeout = 20 * time.Millisecond
	client.sleep = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	_, err := client.Create(context.Background(), model.Request{}, nil)
	assertKind(t, err, model.Cancelled)
	if attempts != 1 || time.Since(start) > time.Second {
		t.Fatalf("retry reset deadline: attempts=%d elapsed=%v", attempts, time.Since(start))
	}
}

func TestRequestSendsFullHistoryWithoutCompactionOrTruncation(t *testing.T) {
	paths := []string{}
	var payload map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		writeComplete(w)
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL + "/custom/v1", Model: "test", APIKey: "secret"}
	items := []model.Item{
		{Kind: model.User, Text: "first"},
		{Kind: model.Assistant, Text: "second"},
		{Kind: model.ToolCallItem, Name: "probe", CallID: "c1", Arguments: json.RawMessage(`{}`)},
		{Kind: model.ToolResultItem, CallID: "c1", Text: `{"status":"succeeded"}`},
		{Kind: model.User, Text: "follow up"},
	}
	_, err := client.Create(context.Background(), model.Request{Items: items}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var input []json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/custom/v1/responses" || len(input) != len(items) {
		t.Fatalf("paths=%v input count=%d", paths, len(input))
	}
	for _, forbidden := range []string{"truncation", "context_management", "previous_response_id", "conversation", "compact"} {
		if _, exists := payload[forbidden]; exists {
			t.Fatalf("unexpected automatic history option %s", forbidden)
		}
	}
	if !strings.Contains(string(input[0]), "first") || !strings.Contains(string(input[len(input)-1]), "follow up") {
		t.Fatal("history was shortened")
	}
}
