// Package model contains provider-independent types used by the agent loop.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"host-scout/pkg/tool"
)

type ItemKind string

const (
	User           ItemKind = "user"
	Assistant      ItemKind = "assistant"
	ToolCallItem   ItemKind = "tool_call"
	ToolResultItem ItemKind = "tool_result"
	Opaque         ItemKind = "provider"
)

// Item has generic semantics even when no provider payload exists. Optional
// ProviderData belongs exclusively to ProviderID; an adapter must reject
// foreign payloads rather than attempting provider migration. A raw payload
// represents this same item, not an additional history item.
type Item struct {
	Kind         ItemKind
	Text         string
	CallID       string
	Name         string
	Arguments    json.RawMessage
	ProviderID   string
	ProviderData json.RawMessage
}

// CloneItems prevents a provider or a snapshot reader from changing history.
func CloneItems(items []Item) []Item {
	copyItems := append([]Item(nil), items...)
	for i := range copyItems {
		copyItems[i].Arguments = append(json.RawMessage(nil), copyItems[i].Arguments...)
		copyItems[i].ProviderData = append(json.RawMessage(nil), copyItems[i].ProviderData...)
	}
	return copyItems
}

type ErrorKind string

const (
	Transport    ErrorKind = "transport"
	HTTP         ErrorKind = "http"
	Protocol     ErrorKind = "protocol"
	Incomplete   ErrorKind = "incomplete"
	RemoteFailed ErrorKind = "remote_failed"
	Cancelled    ErrorKind = "cancelled"
	ContextLimit ErrorKind = "context_limit"
)

// Error deliberately excludes remote bodies, URLs and credentials. Cause is
// available to errors.Is/As, but is never included in the display message.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Detail     string
	Cause      error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("model %s: HTTP %d", e.Kind, e.StatusCode)
	}
	return fmt.Sprintf("model %s: %s", e.Kind, e.Detail)
}
func (e *Error) Unwrap() error { return e.Cause }

// ToolDefinition is kept as a compatibility name; the public definition lives
// in pkg/tool and contains no provider-specific fields.
type ToolDefinition = tool.Definition

type ToolCall struct {
	Name      string
	CallID    string
	Arguments json.RawMessage
}

type Request struct {
	Instructions string
	Items        []Item
	Tools        []tool.Definition
}

type Response struct {
	Items []Item
	Calls []ToolCall
	Text  string
}

// Client emits visible text/refusal synchronously during Create, then returns
// a complete response or an error with no executable partial response. It must
// not retain/call onText after returning. Calls are an execution view of Items.
// Sessions do not support changing providers during a conversation.
type Client interface {
	Create(ctx context.Context, request Request, onText func(string)) (Response, error)
}
