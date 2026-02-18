package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/core/logger"

	"github.com/javi11/sevenzip"
)

// SevenZipBlueprint stores metadata about an uncompressed file inside a 7z archive.
type SevenZipBlueprint struct {
	MainFileName string
	TotalSize    int64
	FileOffset   int64
	Files        []*loader.File
}

// CreateSevenZipBlueprint scans a 7z archive and builds a cached blueprint
// for the best uncompressed video file found.
func CreateSevenZipBlueprint(files []*loader.File, firstVolName string) (*SevenZipBlueprint, error) {
	archiveFiles := filter7zFiles(files)
	parts := filesToParts(archiveFiles)
	mr := NewConcatenatedReaderAt(parts)

	r, err := sevenzip.NewReader(mr, mr.Size())
	if err != nil {
		return nil, fmt.Errorf("failed to open 7z archive: %w", err)
	}

	fileInfos, err := r.ListFilesWithOffsets()
	if err != nil {
		return nil, fmt.Errorf("failed to list 7z files: %w", err)
	}

	bestIdx, bestSize := -1, int64(0)
	for i, fi := range fileInfos {
		if !IsVideoFile(fi.Name) || fi.Compressed || IsSampleFile(fi.Name) {
			continue
		}
		if int64(fi.Size) > bestSize {
			bestIdx = i
			bestSize = int64(fi.Size)
		}
	}

	if bestIdx == -1 {
		return nil, errors.New("no uncompressed media found in 7z")
	}

	fi := fileInfos[bestIdx]
	bp := &SevenZipBlueprint{
		MainFileName: filepath.Base(fi.Name),
		TotalSize:    int64(fi.Size),
		FileOffset:   fi.Offset,
		Files:        archiveFiles,
	}
	logger.Debug("Created 7z blueprint", "name", bp.MainFileName, "offset", bp.FileOffset, "size", bp.TotalSize)

	// Pre-warm the last volume's final segment so end-of-file seeks
	// (MKV Cues / MP4 moov atom) are fast on first play.
	if len(archiveFiles) > 0 {
		lastFile := archiveFiles[len(archiveFiles)-1]
		lastFile.PrewarmSegment(lastFile.SegmentCount() - 1)
	}

	return bp, nil
}

// Open7zStreamFromBlueprint creates a stream from a cached blueprint.
func Open7zStreamFromBlueprint(ctx context.Context, bp *SevenZipBlueprint) (ReadSeekCloser, string, int64, error) {
	if bp == nil || len(bp.Files) == 0 {
		return nil, "", 0, errors.New("invalid 7z blueprint")
	}

	parts := filesToParts(bp.Files)
	streamParts, err := mapOffsetToParts(parts, bp.FileOffset, bp.TotalSize)
	if err != nil {
		return nil, "", 0, err
	}

	vs := NewVirtualStream(ctx, streamParts, bp.TotalSize, 0)
	return vs, bp.MainFileName, bp.TotalSize, nil
}

// --- helpers ---

func filter7zFiles(files []*loader.File) []*loader.File {
	var result []*loader.File
	for _, f := range files {
		lower := strings.ToLower(f.Name())
		if strings.HasSuffix(lower, ".7z") || strings.Contains(lower, ".7z.") {
			result = append(result, f)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name()) < strings.ToLower(result[j].Name())
	})
	return result
}

func filesToParts(files []*loader.File) []Part {
	parts := make([]Part, len(files))
	for i, f := range files {
		f.EnsureSegmentMap()
		parts[i] = Part{Reader: f, Offset: 0, Size: f.Size()}
	}
	return parts
}

// mapOffsetToParts maps a logical file range to physical volume parts.
func mapOffsetToParts(volumes []Part, startOffset, size int64) ([]virtualPart, error) {
	var vParts []virtualPart
	remaining := size
	volOff := startOffset
	var virtualPos int64

	for _, vol := range volumes {
		if remaining <= 0 {
			break
		}
		if volOff >= vol.Size {
			volOff -= vol.Size
			continue
		}

		available := vol.Size - volOff
		take := remaining
		if take > available {
			take = available
		}

		uf, ok := vol.Reader.(UnpackableFile)
		if !ok {
			return nil, fmt.Errorf("volume reader does not implement UnpackableFile")
		}

		vParts = append(vParts, virtualPart{
			VirtualStart: virtualPos,
			VirtualEnd:   virtualPos + take,
			VolFile:      uf,
			VolOffset:    volOff,
		})

		virtualPos += take
		remaining -= take
		volOff = 0
	}

	if remaining > 0 {
		return nil, fmt.Errorf("could not map full file range (missing %d bytes)", remaining)
	}
	return vParts, nil
}

// --- ConcatenatedReaderAt ---

// Part represents a segment of data available via io.ReaderAt.
type Part struct {
	Reader io.ReaderAt
	Offset int64
	Size   int64
}

// ConcatenatedReaderAt presents multiple Parts as a single io.ReaderAt.
type ConcatenatedReaderAt struct {
	parts []Part
	total int64
}

func NewConcatenatedReaderAt(parts []Part) *ConcatenatedReaderAt {
	var total int64
	for _, p := range parts {
		total += p.Size
	}
	return &ConcatenatedReaderAt{parts: parts, total: total}
}

func (c *ConcatenatedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= c.total {
		return 0, io.EOF
	}

	// Find starting part
	partIdx := 0
	partOff := off
	for i, part := range c.parts {
		if partOff < part.Size {
			partIdx = i
			break
		}
		partOff -= part.Size
	}

	totalRead := 0
	for partIdx < len(c.parts) && totalRead < len(p) {
		part := c.parts[partIdx]
		available := part.Size - partOff
		toRead := int64(len(p) - totalRead)
		if toRead > available {
			toRead = available
		}

		n, err := part.Reader.ReadAt(p[totalRead:totalRead+int(toRead)], part.Offset+partOff)
		totalRead += n
		if err != nil && err != io.EOF {
			return totalRead, err
		}

		partIdx++
		partOff = 0
		if totalRead == len(p) {
			break
		}
	}

	if totalRead < len(p) {
		return totalRead, io.EOF
	}
	return totalRead, nil
}

func (c *ConcatenatedReaderAt) Size() int64 { return c.total }
