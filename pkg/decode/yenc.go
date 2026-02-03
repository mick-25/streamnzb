package decode

import (
	"bytes"
	"io"

	"github.com/chrisfarms/yenc"
)

// Decode reads from r, decodes yEnc, and writes to w.
// It returns the number of bytes written and the filename found in the header.
func Decode(r io.Reader, w io.Writer) (int64, string, error) {
	part, err := yenc.Decode(r)
	if err != nil {
		return 0, "", err
	}
	
	n, err := io.Copy(w, bytes.NewReader(part.Body))
	if err != nil {
		return n, "", err
	}
	
	return n, part.Name, nil
}

// Frame represents a decoded segment.
type Frame struct {
	Data     []byte
	FileName string
}

// DecodeToBytes decodes the reader into a byte slice.
func DecodeToBytes(r io.Reader) (*Frame, error) {
	part, err := yenc.Decode(r)
	if err != nil {
		return nil, err
	}
	return &Frame{
		Data:     part.Body,
		FileName: part.Name,
	}, nil
}
