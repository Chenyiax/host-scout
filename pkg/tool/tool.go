// Package tool defines the small public contract for host tools. A tool is
// registered at startup, validates arguments before accessing the host, and
// reports a bounded terminal result. It does not depend on an agent or model
// provider. Call Started immediately before the first host access.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Definition is the model-visible name, description and JSON parameter schema.
type Definition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Tool returns a unified result. An error stops the current operation; use a
// failed Result for a recoverable argument or execution failure.
type Tool interface {
	Definition() Definition
	Run(context.Context, json.RawMessage) (Result, error)
}

// Registry is immutable after construction. Definitions are copied so a tool
// cannot silently alter the schema advertised to the model later.
type Registry struct {
	byName      map[string]Tool
	definitions []Definition
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func NewRegistry(items ...Tool) (*Registry, error) {
	r := &Registry{byName: make(map[string]Tool, len(items))}
	for _, item := range items {
		if item == nil {
			return nil, errors.New("nil tool")
		}
		v := reflect.ValueOf(item)
		switch v.Kind() {
		case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface:
			if v.IsNil() {
				return nil, errors.New("nil tool")
			}
		}
		d := item.Definition()
		var schema map[string]json.RawMessage
		if !validName.MatchString(d.Name) || strings.TrimSpace(d.Description) == "" ||
			json.Unmarshal(d.Parameters, &schema) != nil || schema == nil {
			return nil, fmt.Errorf("invalid tool definition %q", d.Name)
		}
		if _, exists := r.byName[d.Name]; exists {
			return nil, fmt.Errorf("duplicate tool name %q", d.Name)
		}
		d.Parameters = append(json.RawMessage(nil), d.Parameters...)
		r.byName[d.Name] = item
		r.definitions = append(r.definitions, d)
	}
	return r, nil
}

func (r *Registry) Definitions() []Definition {
	definitions := append([]Definition(nil), r.definitions...)
	for i := range definitions {
		definitions[i].Parameters = append(json.RawMessage(nil), definitions[i].Parameters...)
	}
	return definitions
}

func (r *Registry) Get(name string) (Tool, bool) {
	item, ok := r.byName[name]
	return item, ok
}

// Run dispatches to a registered tool. Unknown names never access the host.
func (r *Registry) Run(ctx context.Context, name string, arguments json.RawMessage) (Result, error) {
	item, ok := r.Get(name)
	if !ok {
		return Result{Status: Failed, Code: "unknown_tool"}, nil
	}
	return item.Run(ctx, append(json.RawMessage(nil), arguments...))
}

type startKey struct{}

// WithStart attaches a turn-local observer. A tool calls Started only after
// validation and approval, immediately before host access.
func WithStart(ctx context.Context, start func()) context.Context {
	return context.WithValue(ctx, startKey{}, start)
}

func Started(ctx context.Context) {
	if fn, ok := ctx.Value(startKey{}).(func()); ok {
		fn()
	}
}
