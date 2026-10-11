package cli

import (
	"bufio"
	"context"
	"errors"
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
type lineRequest struct{ reply chan lineReply }

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
	in.mu.Unlock()
	if in.onStop != nil {
		in.onStop()
	}
}
func (in *inputReader) ReadLine(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	req := &lineRequest{reply: make(chan lineReply, 1)}
	if in.terminal {
		in.mu.Lock()
		if in.eof {
			in.mu.Unlock()
			return "", io.EOF
		}
		in.active = req
		in.buffer = nil
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
		}
		in.mu.Unlock()
		return "", ctx.Err()
	case <-in.ctx.Done():
		return "", io.EOF
	}
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
				case 8, 127:
					if len(in.buffer) > 0 {
						_, size := utf8.DecodeLastRune(in.buffer)
						in.buffer = in.buffer[:len(in.buffer)-size]
						_, _ = io.WriteString(in.out, "\b \b")
					}
				default:
					if b >= 32 || b == '\t' {
						if len(in.buffer) >= 1<<20 {
							req.reply <- lineReply{err: errors.New("input exceeds 1 MiB")}
							in.active = nil
							in.buffer = nil
						} else {
							in.buffer = append(in.buffer, b)
							_, _ = in.out.Write([]byte{b})
						}
					}
				}
			}
			in.mu.Unlock()
		}
	}
}
