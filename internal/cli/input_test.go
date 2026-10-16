package cli

import (
	"bytes"
	"context"
	"errors"
	"host-scout/internal/policy"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApprovalPromptMatchesDocumentedPurposeAndExactCommand(t *testing.T) {
	c := controlledTerminal(t, nil)
	var output bytes.Buffer
	decisions := make(chan bool, 1)
	go func() {
		allow, err := (terminalApprover{input: c.in, output: &output}).ApproveCommand(context.Background(), policy.CommandRequest{Argv: []string{"go", "version"}, Purpose: "检查 Go 版本"})
		if err != nil {
			t.Error(err)
		}
		decisions <- allow
	}()
	awaitActive(t, c.in)
	c.send("n\n")
	if <-decisions || !strings.Contains(output.String(), "Model purpose: 检查 Go 版本\nCommand requested: \"go\" \"version\"\nIts output will be sent to the model.\nAllow once? [y/N]") {
		t.Fatalf("unexpected approval: %s", output.String())
	}
}

type controlledInput struct {
	in        *inputReader
	bytes     chan byte
	processed chan struct{}
}

func controlledTerminal(t *testing.T, stop func()) controlledInput {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	bytes := make(chan byte)
	processed := make(chan struct{}, 1)
	in := &inputReader{terminal: true, out: io.Discard, ctx: ctx, cancel: cancel, done: make(chan struct{}), onStop: stop}
	hadPrevious := false
	in.readByte = func(ctx context.Context) (byte, error) {
		if hadPrevious {
			processed <- struct{}{}
		}
		select {
		case b := <-bytes:
			hadPrevious = true
			return b, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	go in.terminalLoop()
	t.Cleanup(func() { cancel(); <-in.done })
	return controlledInput{in, bytes, processed}
}
func awaitActive(t *testing.T, in *inputReader) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		in.mu.Lock()
		active := in.active != nil
		in.mu.Unlock()
		if active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("reader never became active")
		}
		time.Sleep(time.Millisecond)
	}
}
func (c controlledInput) send(text string) {
	for _, b := range []byte(text) {
		c.bytes <- b
		<-c.processed
	}
}
func TestTerminalCancelAndStaleApproval(t *testing.T) {
	c := controlledTerminal(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.in.ReadLine(ctx); result <- err }()
	awaitActive(t, c.in)
	c.send("y")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel needed newline")
	}
	// The trailing newline from the abandoned authorization is not queued.
	c.send("\n")
	decisions := make(chan bool, 1)
	go func() {
		allow, err := (terminalApprover{input: c.in, output: io.Discard}).ApproveCommand(context.Background(), policy.CommandRequest{Argv: []string{"exact"}})
		if err != nil {
			t.Error(err)
		}
		decisions <- allow
	}()
	awaitActive(t, c.in)
	c.send("\n")
	if <-decisions {
		t.Fatal("old y transferred to new approval")
	}
	for _, text := range []string{"Y\n", " yes\n", "message\n"} {
		go func() {
			allow, _ := (terminalApprover{input: c.in, output: io.Discard}).ApproveCommand(context.Background(), policy.CommandRequest{})
			decisions <- allow
		}()
		awaitActive(t, c.in)
		c.send(text)
		if allow := <-decisions; allow != (text == "Y\n") {
			t.Fatalf("decision %q=%v", text, allow)
		}
	}
}
func TestTerminalInterruptAndEOFAreImmediate(t *testing.T) {
	c := controlledTerminal(t, nil)
	result := make(chan error, 1)
	go func() { _, err := c.in.ReadLine(context.Background()); result <- err }()
	awaitActive(t, c.in)
	c.send("partial")
	c.bytes <- 3
	if err := <-result; !errors.Is(err, errInterrupted) {
		t.Fatal(err)
	}
	go func() { _, err := c.in.ReadLine(context.Background()); result <- err }()
	awaitActive(t, c.in)
	c.bytes <- 4
	if err := <-result; !errors.Is(err, io.EOF) || !c.in.EOF() {
		t.Fatal(err)
	}
	if _, err := c.in.ReadLine(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatal("EOF not retained")
	}
}
func TestPipeInputPreservesLinesAndHasOneConsumer(t *testing.T) {
	in, err := newInput(strings.NewReader("first\nsecond\nthird\n"), io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	for _, want := range []string{"first", "second", "third"} {
		got, err := in.ReadLine(context.Background())
		if err != nil || got != want {
			t.Fatalf("%q %v want %q", got, err, want)
		}
	}
	if _, err := in.ReadLine(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	in, err = newInput(pr, io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := in.ReadLine(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	// Supply data to release a possible in-flight cancelled read. ReadLine
	// never launches extra consumers, and subsequent source lines remain ordered.
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.WriteString(pw, "discard-or-next\nnext\n"); _ = pw.Close() }()
	line, err := in.ReadLine(context.Background())
	if err != nil || (line != "discard-or-next" && line != "next") {
		t.Fatalf("%q %v", line, err)
	}
	wg.Wait()
}
func TestNonTerminalAuthorizationDoesNotConsumeChat(t *testing.T) {
	in, _ := newInput(strings.NewReader("followup\n"), io.Discard, nil)
	defer in.Close()
	allow, err := (terminalApprover{input: in, output: io.Discard}).ApproveCommand(context.Background(), policy.CommandRequest{Argv: []string{"echo"}})
	if err != nil || allow {
		t.Fatal("nonterminal granted permission")
	}
	if line, err := in.ReadLine(context.Background()); err != nil || line != "followup" {
		t.Fatal("approval consumed chat")
	}
}

func TestTerminalUnicodeEditingAndCursor(t *testing.T) {
	c := controlledTerminal(t, nil)
	var out bytes.Buffer
	c.in.out = &out
	c.in.editing = true
	result := make(chan string, 1)
	go func() { line, _ := c.in.ReadLinePrompt(context.Background(), "scout> ", true); result <- line }()
	awaitActive(t, c.in)
	c.send("ab中e\u0301\x1b[D\x1b[DX\x1b[H\x1b[3~\x1b[F\x7f\n")
	if got := <-result; got != "bX中" {
		t.Fatalf("edited line %q", got)
	}
	if !strings.Contains(out.String(), "\x1b[3D") || !strings.Contains(out.String(), "bX中") {
		t.Fatalf("redraw %q", out.String())
	}
}

func TestHistoryDraftAndApprovalIsolation(t *testing.T) {
	c := controlledTerminal(t, nil)
	c.in.Remember("first")
	c.in.Remember("second")
	result := make(chan string, 1)
	go func() { line, _ := c.in.ReadLinePrompt(context.Background(), "scout> ", true); result <- line }()
	awaitActive(t, c.in)
	c.send("draft\x1b[A\x1b[A\x1b[B\x1b[B\n")
	if got := <-result; got != "draft" {
		t.Fatalf("draft %q", got)
	}
	go func() {
		line, _ := c.in.ReadLinePrompt(context.Background(), "Allow once? [y/N] ", false)
		result <- line
	}()
	awaitActive(t, c.in)
	c.send("\x1b[Ay\n")
	if got := <-result; got != "y" {
		t.Fatalf("approval retrieved history: %q", got)
	}
	c.in.ClearHistory()
	go func() { line, _ := c.in.ReadLinePrompt(context.Background(), "scout> ", true); result <- line }()
	awaitActive(t, c.in)
	c.send("\x1b[A\n")
	if got := <-result; got != "" {
		t.Fatalf("clear retained history: %q", got)
	}
}
