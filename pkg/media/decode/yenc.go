package decode

import (
	"bytes"
	"errors"
	"io"

	"github.com/javi11/rapidyenc"
)

// normalizeCRLF wraps r to convert lone LF to CRLF before rapidyenc.
// rapidyenc expects CRLF; some NNTP servers send LF-only.
type crlfReader struct {
	r     io.Reader
	buf   []byte
	last  byte
	off   int
}

func (c *crlfReader) Read(p []byte) (int, error) {
	out := 0
	for out < len(p) {
		if c.off < len(c.buf) {
			b := c.buf[c.off]
			c.off++
			if b == '\n' && c.last != '\r' {
				p[out] = '\r'
				out++
				c.last = '\r'
				if out >= len(p) {
					c.off-- // put \n back for next Read
					return out, nil
				}
			}
			p[out] = b
			out++
			c.last = b
			continue
		}
		c.buf = make([]byte, 4096)
		n, err := c.r.Read(c.buf)
		c.buf = c.buf[:n]
		c.off = 0
		if n == 0 {
			return out, err
		}
	}
	return out, nil
}

func normalizeCRLF(r io.Reader) io.Reader { return &crlfReader{r: r} }

// Decode reads from r, decodes yEnc, and writes to w.
// It returns the number of bytes written and the filename found in the header.
func Decode(r io.Reader, w io.Writer) (int64, string, error) {
	dec := rapidyenc.NewDecoder(normalizeCRLF(r))
	n, err := io.Copy(w, dec)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, "", err
	}
	return n, dec.Meta.FileName, nil
}

// Frame represents a decoded segment.
type Frame struct {
	Data     []byte
	FileName string
}

// DecodeToBytes decodes the reader into a byte slice.
func DecodeToBytes(r io.Reader) (*Frame, error) {
	dec := rapidyenc.NewDecoder(normalizeCRLF(r))
	buf := new(bytes.Buffer)
	_, err := io.Copy(buf, dec)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &Frame{
		Data:     buf.Bytes(),
		FileName: dec.Meta.FileName,
	}, nil
}
