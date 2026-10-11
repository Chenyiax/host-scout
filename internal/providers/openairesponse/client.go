// Package openairesponse implements only POST /responses with HTTP/SSE.
// Custom endpoints must implement this protocol; no fallback is attempted.
// Ordered raw output is replayed once for stateless store=false continuation.
// Every valid history item is sent on each request; this adapter does not
// compact, summarize, truncate or call Conversations. A custom endpoint must
// support Responses-compatible SSE and completed output items.
// Explicit context_length_exceeded errors are classified without exposing
// remote bodies; output-token limits remain incomplete responses. Retries only
// repeat the current model request before any SSE data, never run tools, and
// cannot guarantee the remote computation happened exactly once.
package openairesponse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"host-scout/internal/model"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxEventBytes = 8 << 20
const maxErrorBytes = 16 << 10
const ProviderID = "openai-responses"

type Client struct {
	BaseURL     string
	Model       string
	APIKey      string
	HTTPClient  *http.Client
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	callTimeout time.Duration
}

func failure(kind model.ErrorKind, detail string, cause error) error {
	return &model.Error{Kind: kind, Detail: detail, Cause: cause}
}
func (c *Client) Create(ctx context.Context, request model.Request, onText func(string)) (model.Response, error) {
	timeout := c.callTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if ctx.Err() != nil {
		return model.Response{}, failure(model.Cancelled, "request cancelled", ctx.Err())
	}
	input := make([]json.RawMessage, 0, len(request.Items))
	for _, item := range request.Items {
		encoded, err := encodeItem(item)
		if err != nil {
			return model.Response{}, err
		}
		input = append(input, encoded)
	}
	definitions := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		definitions = append(definitions, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": true,
		})
	}
	body, err := json.Marshal(map[string]any{
		"model": c.Model, "instructions": request.Instructions, "input": input, "tools": definitions,
		"tool_choice": "auto", "parallel_tool_calls": false, "store": false, "stream": true,
	})
	if err != nil {
		return model.Response{}, failure(model.Protocol, "invalid request", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err, retry, retryAfter := c.createAttempt(ctx, body, onText)
		if err == nil || !retry || attempt == 2 {
			return result, err
		}
		wait := time.Duration(1<<attempt) * 500 * time.Millisecond
		if serverWait, valid := c.retryAfter(retryAfter); valid && serverWait > wait {
			wait = serverWait
		}
		if wait > 30*time.Second {
			return model.Response{}, err
		}
		if waitErr := c.wait(ctx, wait); waitErr != nil {
			return model.Response{}, failure(model.Cancelled, "request cancelled", waitErr)
		}
	}
	return model.Response{}, failure(model.Transport, "retry budget exhausted", nil)
}

func (c *Client) createAttempt(ctx context.Context, body []byte, onText func(string)) (model.Response, error, bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return model.Response{}, failure(model.Protocol, "invalid endpoint", err), false, ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return model.Response{}, failure(model.Cancelled, "request cancelled", ctx.Err()), false, ""
		}
		return model.Response{}, failure(model.Transport, "request failed", err), transient(err), ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		if isContextLimit(body) {
			return model.Response{}, &model.Error{Kind: model.ContextLimit, StatusCode: resp.StatusCode}, false, ""
		}
		return model.Response{}, &model.Error{Kind: model.HTTP, StatusCode: resp.StatusCode}, retryableStatus(resp.StatusCode), resp.Header.Get("Retry-After")
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return model.Response{}, failure(model.Protocol, "expected SSE response", nil), false, ""
	}
	seenData := false
	result, err := decodeStreamWithState(resp.Body, onText, &seenData)
	if ctx.Err() != nil {
		return model.Response{}, failure(model.Cancelled, "request cancelled", ctx.Err()), false, ""
	}
	var modelErr *model.Error
	return result, err, !seenData && errors.As(err, &modelErr) && modelErr.Kind == model.Transport && transient(modelErr.Cause), ""
}

func retryableStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func transient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func (c *Client) retryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds >= 0 {
		if seconds > 30 {
			return 31 * time.Second, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(header); err == nil {
		now := time.Now
		if c.now != nil {
			now = c.now
		}
		return max(0, date.Sub(now())), true
	}
	return 0, false
}

func (c *Client) wait(ctx context.Context, delay time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func encodeItem(item model.Item) (json.RawMessage, error) {
	if len(item.ProviderData) != 0 {
		if item.ProviderID != ProviderID || !json.Valid(item.ProviderData) {
			return nil, failure(model.Protocol, "invalid or foreign provider item", nil)
		}
		return append(json.RawMessage(nil), item.ProviderData...), nil
	}
	switch item.Kind {
	case model.User, model.Assistant:
		return json.Marshal(map[string]string{"role": string(item.Kind), "content": item.Text})
	case model.ToolCallItem:
		if item.Name == "" || item.CallID == "" {
			return nil, failure(model.Protocol, "function call missing name or call ID", nil)
		}
		return json.Marshal(map[string]string{"type": "function_call", "name": item.Name, "call_id": item.CallID, "arguments": string(item.Arguments)})
	case model.ToolResultItem:
		if item.CallID == "" {
			return nil, failure(model.Protocol, "tool result has no call ID", nil)
		}
		return json.Marshal(map[string]string{"type": "function_call_output", "call_id": item.CallID, "output": item.Text})
	default:
		return nil, failure(model.Protocol, "unsupported conversation item", nil)
	}
}

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Response json.RawMessage `json:"response"`
}

func decodeStream(reader io.Reader, onText func(string)) (model.Response, error) {
	return decodeStreamWithState(reader, onText, nil)
}

func decodeStreamWithState(reader io.Reader, onText func(string), seenData *bool) (model.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes+1024)
	var data []string
	size := 0
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if len(line) > maxEventBytes+6 {
			return model.Response{}, failure(model.Protocol, "SSE line exceeds 8 MiB", nil)
		}
		if line == "" {
			if len(data) == 0 {
				continue
			}
			payload := strings.Join(data, "\n")
			data, size = nil, 0
			if payload == "[DONE]" {
				continue
			}
			var event streamEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return model.Response{}, failure(model.Protocol, "invalid SSE JSON", err)
			}
			switch event.Type {
			case "response.output_text.delta", "response.refusal.delta":
				if onText != nil && event.Delta != "" {
					onText(event.Delta)
				}
			case "response.completed":
				return decodeCompleted(event.Response)
			case "response.incomplete":
				var details struct {
					IncompleteDetails struct {
						Reason string `json:"reason"`
					} `json:"incomplete_details"`
				}
				_ = json.Unmarshal(event.Response, &details)
				reason := details.IncompleteDetails.Reason
				if reason != "max_output_tokens" && reason != "content_filter" {
					reason = "unspecified"
				}
				return model.Response{}, failure(model.Incomplete, "response.incomplete ("+reason+")", nil)
			case "response.failed", "error":
				if isContextLimit([]byte(payload)) {
					return model.Response{}, failure(model.ContextLimit, "context length exceeded", nil)
				}
				return model.Response{}, failure(model.RemoteFailed, "response failed", nil)
			}
			continue
		}
		if strings.HasPrefix(line, "data:") || line == "data" {
			if seenData != nil {
				*seenData = true
			}
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data"), ":")
			value = strings.TrimPrefix(value, " ")
			if len(data) > 0 {
				size++
			}
			size += len(value)
			if size > maxEventBytes {
				return model.Response{}, failure(model.Protocol, "SSE event exceeds 8 MiB", nil)
			}
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return model.Response{}, failure(model.Protocol, "SSE line exceeds 8 MiB", err)
		}
		return model.Response{}, failure(model.Transport, "read model stream", err)
	}
	if len(data) != 0 {
		return model.Response{}, failure(model.Protocol, "model stream ended during an event", nil)
	}
	return model.Response{}, failure(model.Protocol, "model stream ended without response.completed", nil)
}

// isContextLimit accepts only an explicit stable remote code. The caller
// bounds the raw input; remote messages and bodies are never displayed.
func isContextLimit(raw []byte) bool {
	var envelope struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	return envelope.Code == "context_length_exceeded" || envelope.Error.Code == "context_length_exceeded" || envelope.Response.Error.Code == "context_length_exceeded"
}
func decodeCompleted(raw json.RawMessage) (model.Response, error) {
	var body struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return model.Response{}, failure(model.Protocol, "invalid completed response", err)
	}
	if body.Status != "completed" {
		return model.Response{}, failure(model.Protocol, "invalid completed status", nil)
	}
	var result model.Response
	for _, rawItem := range body.Output {
		var wire struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if err := json.Unmarshal(rawItem, &wire); err != nil || wire.Type == "" {
			return model.Response{}, failure(model.Protocol, "invalid output item", err)
		}
		item := model.Item{Kind: model.Opaque, ProviderID: ProviderID, ProviderData: append(json.RawMessage(nil), rawItem...)}
		switch wire.Type {
		case "function_call":
			if wire.Name == "" || wire.CallID == "" {
				return model.Response{}, failure(model.Protocol, "function call missing name or call ID", nil)
			}
			item.Kind, item.Name, item.CallID, item.Arguments = model.ToolCallItem, wire.Name, wire.CallID, json.RawMessage(wire.Arguments)
			result.Calls = append(result.Calls, model.ToolCall{Name: wire.Name, CallID: wire.CallID, Arguments: append(json.RawMessage(nil), item.Arguments...)})
		case "message":
			item.Kind = model.Assistant
			for _, content := range wire.Content {
				if content.Type == "output_text" {
					item.Text += content.Text
				}
				if content.Type == "refusal" {
					item.Text += content.Refusal
				}
			}
			result.Text += item.Text
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}
