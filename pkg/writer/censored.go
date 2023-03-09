package writer

import (
	"io"
)

type censoredWriter struct {
	delegate io.Writer
	censor   func(string) string
}

// NewCensoredWriter creates a new writer which censors sensitive information.
func NewCensoredWriter(delegate io.Writer, censor func(string) string) io.Writer {
	return &censoredWriter{
		delegate: delegate,
		censor:   censor,
	}
}

func (c *censoredWriter) Write(b []byte) (int, error) {
	return c.delegate.Write([]byte(c.censor(string(b))))
}
