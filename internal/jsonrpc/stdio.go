package jsonrpc

import (
	"bufio"
	"io"
)

// stdioTransport frames messages as newline-delimited JSON over a child
// process's stdio pipes.
type stdioTransport struct {
	stdin  io.WriteCloser
	reader *bufio.Reader
}

func (t *stdioTransport) Read() ([]byte, error) {
	for {
		line, err := t.reader.ReadBytes('\n')
		if len(line) > 0 {
			return trimNewline(line), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (t *stdioTransport) Write(b []byte) error {
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *stdioTransport) Close() error { return t.stdin.Close() }

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// AttachStdio runs JSON-RPC over a child process's stdio pipes. onStderr
// observes the process's raw stderr lines when stderr is not nil; kill stops
// the process tree.
func AttachStdio(stdin io.WriteCloser, stdout, stderr io.ReadCloser, kill func(), onStderr func(string), handlers Handlers) *Conn {
	tr := &stdioTransport{stdin: stdin, reader: bufio.NewReaderSize(stdout, 1<<16)}
	c := NewConn(tr, handlers, kill)
	if stderr != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			r := bufio.NewReader(stderr)
			for {
				line, err := r.ReadString('\n')
				if onStderr != nil && line != "" {
					onStderr(line)
				}
				if err != nil {
					return
				}
			}
		}()
	}
	return c
}
