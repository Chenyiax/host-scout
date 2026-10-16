package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/rivo/uniseg"
	"golang.org/x/term"
	"io"
	"os"
	"sync"
	"unicode/utf8"
)

var errInterrupted = errors.New("input interrupted")

type lineReply struct {
	line string
	err  error
}
type lineRequest struct {
	reply   chan lineReply
	prompt  string
	history bool
}

// inputReader owns exactly one input consumer for its lifetime. Pipes scan on
// demand, preserving queued lines. Raw terminals consume continuously, discard
// input while busy, and route Ctrl+C/EOF without requiring a newline.
type inputReader struct {
	source    io.Reader
	out       io.Writer
	terminal  bool
	file      *os.File
	restore   *term.State
	ctx       context.Context
	cancel    context.CancelFunc
	pipeLines chan lineReply
	done      chan struct{}
	mu        sync.Mutex
	active    *lineRequest
	buffer    []byte
	cursor    int
	escape    []byte
	utf8Buf   []byte
	editing   bool
	history   []string
	historyAt int
	draft     string
	eof       bool
	pipeErr   error
	onStop    func()
	readByte  func(context.Context) (byte, error)
}

func newInput(source io.Reader, out io.Writer, onStop func()) (*inputReader, error) {
	ctx, cancel := context.WithCancel(context.Background())
	in := &inputReader{source: source, out: out, ctx: ctx, cancel: cancel, pipeLines: make(chan lineReply), done: make(chan struct{}), onStop: onStop}
	if file, ok := source.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		state, err := term.MakeRaw(int(file.Fd()))
		if err != nil {
			cancel()
			return nil, err
		}
		in.terminal, in.file, in.restore = true, file, state
	}
	if in.terminal {
		go in.terminalLoop()
	} else {
		go in.pipeLoop()
	}
	return in, nil
}
func (in *inputReader) Close() {
	in.cancel()
	if in.terminal {
		<-in.done
		_ = term.Restore(int(in.file.Fd()), in.restore)
	}
	// An arbitrary caller-owned Reader may not be interruptible. There is only
	// one session-scoped pipe consumer, never one blocked goroutine per turn.
}
func (in *inputReader) EOF() bool { in.mu.Lock(); defer in.mu.Unlock(); return in.eof }
func (in *inputReader) stop(eof bool) {
	in.mu.Lock()
	if eof {
		in.eof = true
	}
	if in.active != nil {
		err := errInterrupted
		if eof {
			err = io.EOF
		}
		in.active.reply <- lineReply{err: err}
		in.active = nil
	}
	in.buffer = nil
	in.cursor = 0
	in.escape = nil
	in.utf8Buf = nil
	in.mu.Unlock()
	if in.onStop != nil {
		in.onStop()
	}
}
func (in *inputReader) ReadLine(ctx context.Context) (string, error) {
	return in.ReadLinePrompt(ctx, "", false)
}
func (in *inputReader) ReadLinePrompt(ctx context.Context, prompt string, history bool) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	req := &lineRequest{reply: make(chan lineReply, 1), prompt: prompt, history: history}
	if in.terminal {
		in.mu.Lock()
		if in.eof {
			in.mu.Unlock()
			return "", io.EOF
		}
		in.active = req
		in.buffer = nil
		in.cursor = 0
		in.escape = nil
		in.utf8Buf = nil
		in.historyAt = len(in.history)
		in.draft = ""
		in.mu.Unlock()
	} else {
		select {
		case reply, ok := <-in.pipeLines:
			if !ok {
				in.mu.Lock()
				pipeErr := in.pipeErr
				in.mu.Unlock()
				if pipeErr != nil {
					return "", pipeErr
				}
				return "", io.EOF
			}
			return reply.line, reply.err
		case <-ctx.Done():
			return "", ctx.Err()
		case <-in.done:
			return "", io.EOF
		}
	}
	select {
	case reply := <-req.reply:
		return reply.line, reply.err
	case <-ctx.Done():
		in.mu.Lock()
		if in.active == req {
			in.active = nil
			in.buffer = nil
			in.cursor = 0
		}
		in.mu.Unlock()
		return "", ctx.Err()
	case <-in.ctx.Done():
		return "", io.EOF
	}
}

func (in *inputReader) Remember(line string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if line == "" || len(line) > 4096 {
		return
	}
	if len(in.history) == 0 || in.history[len(in.history)-1] != line {
		in.history = append(in.history, line)
	}
	total := 0
	for _, item := range in.history {
		total += len(item)
	}
	for len(in.history) > 100 || total > 64<<10 {
		total -= len(in.history[0])
		in.history = in.history[1:]
	}
}
func (in *inputReader) ClearHistory() {
	in.mu.Lock()
	in.history = nil
	in.historyAt = 0
	in.draft = ""
	in.mu.Unlock()
}

func previousCluster(value []byte, cursor int) int {
	start := 0
	for start < cursor {
		_, rest, _, _ := uniseg.FirstGraphemeCluster(value[start:cursor], -1)
		next := cursor - len(rest)
		if next >= cursor {
			return start
		}
		start = next
	}
	return 0
}
func nextCluster(value []byte, cursor int) int {
	if cursor >= len(value) {
		return len(value)
	}
	cluster, _, _, _ := uniseg.FirstGraphemeCluster(value[cursor:], -1)
	return cursor + len(cluster)
}
func (in *inputReader) redraw(req *lineRequest) {
	if !in.editing {
		return
	}
	value := string(in.buffer)
	_, _ = fmt.Fprintf(in.out, "\r\x1b[2K%s%s", req.prompt, value)
	if cells := uniseg.StringWidth(string(in.buffer[in.cursor:])); cells > 0 {
		_, _ = fmt.Fprintf(in.out, "\x1b[%dD", cells)
	}
}
func (in *inputReader) historyMove(req *lineRequest, delta int) {
	if !req.history || len(in.history) == 0 {
		return
	}
	if delta < 0 {
		if in.historyAt == 0 {
			return
		}
		if in.historyAt == len(in.history) {
			in.draft = string(in.buffer)
		}
		in.historyAt--
		in.buffer = []byte(in.history[in.historyAt])
	} else {
		if in.historyAt >= len(in.history) {
			return
		}
		in.historyAt++
		if in.historyAt == len(in.history) {
			in.buffer = []byte(in.draft)
		} else {
			in.buffer = []byte(in.history[in.historyAt])
		}
	}
	in.cursor = len(in.buffer)
	in.redraw(req)
}
func (in *inputReader) editByte(req *lineRequest, b byte) {
	if len(in.escape) > 0 || b == 27 {
		in.escape = append(in.escape, b)
		if len(in.escape) == 1 {
			return
		}
		if len(in.escape) > 12 {
			in.escape = nil
			return
		}
		if in.escape[1] != '[' && in.escape[1] != 'O' {
			in.escape = nil
			return
		}
		if len(in.escape) == 2 {
			return
		}
		if b < 0x40 || b > 0x7e {
			return
		}
		seq := string(in.escape)
		in.escape = nil
		switch seq {
		case "\x1b[D":
			in.cursor = previousCluster(in.buffer, in.cursor)
		case "\x1b[C":
			in.cursor = nextCluster(in.buffer, in.cursor)
		case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
			in.cursor = 0
		case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
			in.cursor = len(in.buffer)
		case "\x1b[3~":
			end := nextCluster(in.buffer, in.cursor)
			in.buffer = append(in.buffer[:in.cursor], in.buffer[end:]...)
		case "\x1b[A":
			in.historyMove(req, -1)
			return
		case "\x1b[B":
			in.historyMove(req, 1)
			return
		}
		in.redraw(req)
		return
	}
	if b == 8 || b == 127 {
		start := previousCluster(in.buffer, in.cursor)
		if start < in.cursor {
			in.buffer = append(in.buffer[:start], in.buffer[in.cursor:]...)
			in.cursor = start
			in.redraw(req)
		}
		return
	}
	if b < 32 && b != '\t' {
		return
	}
	in.utf8Buf = append(in.utf8Buf, b)
	if !utf8.FullRune(in.utf8Buf) {
		return
	}
	if !utf8.Valid(in.utf8Buf) {
		in.utf8Buf = nil
		return
	}
	if len(in.buffer)+len(in.utf8Buf) > 1<<20 {
		req.reply <- lineReply{err: errors.New("input exceeds 1 MiB")}
		in.active = nil
		in.buffer = nil
		in.utf8Buf = nil
		return
	}
	appendAtEnd := in.cursor == len(in.buffer)
	updated := make([]byte, 0, len(in.buffer)+len(in.utf8Buf))
	updated = append(updated, in.buffer[:in.cursor]...)
	updated = append(updated, in.utf8Buf...)
	updated = append(updated, in.buffer[in.cursor:]...)
	in.buffer = updated
	in.cursor += len(in.utf8Buf)
	if appendAtEnd && in.editing {
		_, _ = in.out.Write(in.utf8Buf)
	} else {
		in.redraw(req)
	}
	in.utf8Buf = nil
}
func (in *inputReader) pipeLoop() {
	defer close(in.done)
	defer close(in.pipeLines)
	scanner := bufio.NewScanner(in.source)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		select {
		case in.pipeLines <- lineReply{line: scanner.Text()}:
		case <-in.ctx.Done():
			return
		}
	}
	in.mu.Lock()
	in.pipeErr = scanner.Err()
	in.mu.Unlock()
	in.stop(true)
}
func (in *inputReader) terminalLoop() {
	defer close(in.done)
	for {
		var b byte
		var err error
		if in.readByte != nil {
			b, err = in.readByte(in.ctx)
		} else {
			b, err = terminalByte(in.ctx, in.file)
		}
		if err != nil {
			if in.ctx.Err() == nil {
				in.stop(true)
			}
			return
		}
		switch b {
		case 3:
			in.stop(false)
		case 4:
			in.mu.Lock()
			empty := len(in.buffer) == 0
			in.mu.Unlock()
			if empty {
				in.stop(true)
				return
			}
		default:
			in.mu.Lock()
			req := in.active
			if req != nil {
				switch b {
				case '\r', '\n':
					_, _ = io.WriteString(in.out, "\n")
					req.reply <- lineReply{line: string(in.buffer)}
					in.active = nil
					in.buffer = nil
				default:
					in.editByte(req, b)
				}
			}
			in.mu.Unlock()
		}
	}
}
