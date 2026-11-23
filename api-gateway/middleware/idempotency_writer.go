package middleware

import (
	"bytes"
	"net/http"
)

// captureWriter streams the response to the client while recording status,
// headers and body so a completed response can be stored. Once the body 
// exceeds limit it stops buffering and marks the response uncacheable.
type captureWriter struct {
	http.ResponseWriter
	status int
	header http.Header
	buf    bytes.Buffer
	limit  int
	tooBig bool
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
		c.header = c.Header().Clone()
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if !c.tooBig {
		if c.buf.Len()+len(p) > c.limit {
			c.tooBig = true
			c.buf.Reset()
		} else {
			c.buf.Write(p)
		}
	}
	return c.ResponseWriter.Write(p)
}

// Flush passes through so streaming handlers keep working.
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
