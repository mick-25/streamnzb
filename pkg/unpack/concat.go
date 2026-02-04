package unpack

import (
	"io"
)

type Part struct {
	Reader io.ReaderAt
	Offset int64 // Start offset in the underlying reader
	Size   int64 // Size of this part
}

type ConcatenatedReaderAt struct {
	parts []Part
	total int64
}

func NewConcatenatedReaderAt(parts []Part) *ConcatenatedReaderAt {
	var total int64
	for _, p := range parts {
		total += p.Size
	}
	return &ConcatenatedReaderAt{
		parts: parts,
		total: total,
	}
}

func (c *ConcatenatedReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= c.total {
		return 0, io.EOF
	}

	// Find starting part
	var currentPartIdx int
	var currentPartOff int64 // Offset relative to the start of the *part* (0-based)
	
	remainingOff := off
	for i, part := range c.parts {
		if remainingOff < part.Size {
			currentPartIdx = i
			currentPartOff = remainingOff
			break
		}
		remainingOff -= part.Size
	}

	totalRead := 0
	for currentPartIdx < len(c.parts) && totalRead < len(p) {
		part := c.parts[currentPartIdx]
		
		available := part.Size - currentPartOff
		toRead := int64(len(p) - totalRead)
		if toRead > available {
			toRead = available
		}

		// Read from underlying reader at (PartOffset + currentPartOff)
		readN, readErr := part.Reader.ReadAt(p[totalRead:totalRead+int(toRead)], part.Offset + currentPartOff)
		totalRead += readN
		
		if readErr != nil && readErr != io.EOF {
			return totalRead, readErr
		}
		
		// Move to next part
		currentPartIdx++
		currentPartOff = 0
		
		if totalRead == len(p) {
			break
		}
	}
	
	if totalRead < len(p) {
		return totalRead, io.EOF
	}
	return totalRead, nil
}

func (c *ConcatenatedReaderAt) Size() int64 {
	return c.total
}

// Helper to convert to ReadSeeker/ReadCloser
type ReadSeekerCloser struct {
	*io.SectionReader
	closer io.Closer
}

func (r *ReadSeekerCloser) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

func NewReadSeekerCloser(ra io.ReaderAt, size int64, closer io.Closer) io.ReadCloser {
	return &ReadSeekerCloser{
		SectionReader: io.NewSectionReader(ra, 0, size),
		closer:        closer,
	}
}
