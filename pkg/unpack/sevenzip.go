package unpack

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"

	"github.com/javi11/sevenzip"
)

// SevenZipBlueprint stores metadata about an uncompressed 7z archive
type SevenZipBlueprint struct {
	MainFileName string
	TotalSize    int64
	FileOffset   int64          // Offset of the file data within the archive
	Files        []*loader.File // All 7z volume files
}

// Open7zStream opens an UNCOMPRESSED 7z archive and returns a stream
// Compressed 7z archives are NOT supported for streaming
func Open7zStream(files []*loader.File, firstVolName string) (ReadSeekCloser, string, int64, error) {
	// Filter 7z files
	var archiveFiles []*loader.File
	for _, f := range files {
		name := strings.ToLower(f.Name())
		if strings.HasSuffix(name, ".7z") || strings.Contains(name, ".7z.") {
			archiveFiles = append(archiveFiles, f)
		}
	}

	if len(archiveFiles) == 0 {
		return nil, "", 0, errors.New("no 7z files found")
	}

	// Sort files to ensure correct order/concatenation
	// This is critical for 7z split archives
	sort.Slice(archiveFiles, func(i, j int) bool {
		return strings.ToLower(archiveFiles[i].Name()) < strings.ToLower(archiveFiles[j].Name())
	})

	// Convert files to Parts for concatenation
	var parts []Part
	for _, f := range archiveFiles {
		parts = append(parts, Part{
			Reader: f,
			Offset: 0,
			Size:   f.Size(),
		})
	}

	mr := NewConcatenatedReaderAt(parts)

	// Open 7z archive to read structure using javi11's fork
	r, err := sevenzip.NewReader(mr, mr.Size())
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to open 7z archive: %w", err)
	}

	// List files with their offsets (this is the key feature from javi11's fork!)
	fileInfos, err := r.ListFilesWithOffsets()
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to list 7z files: %w", err)
	}

	// Find main video file
	for _, fi := range fileInfos {
		name := fi.Name
		if IsVideoFile(name) {
			// Check if file is compressed
			if fi.Compressed {
				logger.Debug("Skipping compressed file in 7z", "name", filepath.Base(name))
				continue
			}

			if IsSampleFile(name) {
				logger.Debug("Skipping sample file in 7z", "name", filepath.Base(name))
				continue
			}

			logger.Debug("Found uncompressed video file in 7z", "name", filepath.Base(name), "offset", fi.Offset, "size", fi.Size)

			// Map the logical file range to physical volume parts
			streamParts, err := mapOffsetToParts(parts, int64(fi.Offset), int64(fi.Size))
			if err != nil {
				return nil, "", 0, err
			}

			// Create VirtualStream (uses SmartStream internally)
			vs := NewVirtualStream(streamParts, int64(fi.Size), 0)

			return vs, filepath.Base(name), int64(fi.Size), nil
		}
	}

	return nil, "", 0, errors.New("no uncompressed media found in 7z")
}

// CreateSevenZipBlueprint creates a blueprint for caching
func CreateSevenZipBlueprint(files []*loader.File, firstVolName string) (*SevenZipBlueprint, error) {
	// Filter 7z files
	var archiveFiles []*loader.File
	for _, f := range files {
		name := strings.ToLower(f.Name())
		if strings.HasSuffix(name, ".7z") || strings.Contains(name, ".7z.") {
			archiveFiles = append(archiveFiles, f)
		}
	}

	// Sort files to ensure correct order
	sort.Slice(archiveFiles, func(i, j int) bool {
		return strings.ToLower(archiveFiles[i].Name()) < strings.ToLower(archiveFiles[j].Name())
	})

	// Convert to parts
	var parts []Part
	for _, f := range archiveFiles {
		parts = append(parts, Part{
			Reader: f,
			Offset: 0,
			Size:   f.Size(),
		})
	}

	mr := NewConcatenatedReaderAt(parts)

	// Open archive
	r, err := sevenzip.NewReader(mr, mr.Size())
	if err != nil {
		return nil, err
	}

	// List files with offsets
	fileInfos, err := r.ListFilesWithOffsets()
	if err != nil {
		return nil, err
	}

	// Find best video file
	bestIdx := -1
	var bestSize int64

	for i, fi := range fileInfos {
		name := fi.Name
		if IsVideoFile(name) {
			if fi.Compressed {
				continue // Skip compressed files
			}

			if IsSampleFile(name) {
				logger.Debug("Skipping sample file in blueprint creation", "name", name)
				continue
			}

			// Scoring logic:
			// 1. Prefer larger files

			if bestIdx == -1 {
				bestIdx = i
				bestSize = int64(fi.Size)
				continue
			}

			// Both are real files, pick largest
			if int64(fi.Size) > bestSize {
				bestIdx = i
				bestSize = int64(fi.Size)
			}
		}
	}

	if bestIdx != -1 {
		fi := fileInfos[bestIdx]
		bp := &SevenZipBlueprint{
			MainFileName: filepath.Base(fi.Name),
			TotalSize:    int64(fi.Size),
			FileOffset:   fi.Offset,
			Files:        archiveFiles,
		}

		logger.Debug("Created 7z blueprint", "name", bp.MainFileName, "offset", bp.FileOffset, "size", bp.TotalSize)
		return bp, nil
	}

	return nil, errors.New("no uncompressed media found in 7z")
}

// Open7zStreamFromBlueprint opens from cached blueprint
func Open7zStreamFromBlueprint(bp *SevenZipBlueprint) (ReadSeekCloser, string, int64, error) {
	if bp == nil || len(bp.Files) == 0 {
		return nil, "", 0, errors.New("invalid 7z blueprint")
	}

	logger.Debug("Using cached 7z blueprint", "name", bp.MainFileName)

	// Convert files to parts
	var parts []Part
	for _, f := range bp.Files {
		parts = append(parts, Part{
			Reader: f,
			Offset: 0,
			Size:   f.Size(),
		})
	}

	// Map offset
	streamParts, err := mapOffsetToParts(parts, bp.FileOffset, bp.TotalSize)
	if err != nil {
		return nil, "", 0, err
	}

	vs := NewVirtualStream(streamParts, bp.TotalSize, 0)

	return vs, bp.MainFileName, bp.TotalSize, nil
}

// mapOffsetToParts slices the physical volume list into VirtualParts for a specific file range
func mapOffsetToParts(volumes []Part, startOffset, size int64) ([]virtualPart, error) {
	var vParts []virtualPart

	currentVolOffset := startOffset
	remainingSize := size
	virtualPos := int64(0)

	for _, vol := range volumes {
		if remainingSize <= 0 {
			break
		}

		// Skip volume if startOffset is completely past it
		if currentVolOffset >= vol.Size {
			currentVolOffset -= vol.Size
			continue
		}

		// Calculate how much we can take from this volume
		availableInVol := vol.Size - currentVolOffset
		take := remainingSize
		if take > availableInVol {
			take = availableInVol
		}

		// Ensure we act on a valid UnpackableFile
		// Part.Reader holds the *loader.File which adheres to UnpackableFile
		uf, ok := vol.Reader.(UnpackableFile)
		if !ok {
			return nil, fmt.Errorf("volume reader does not satisfy UnpackableFile")
		}

		vParts = append(vParts, virtualPart{
			VirtualStart: virtualPos,
			VirtualEnd:   virtualPos + take,
			VolFile:      uf,
			VolOffset:    currentVolOffset,
		})

		virtualPos += take
		remainingSize -= take
		currentVolOffset = 0 // For subsequent volumes, we start from 0
	}

	if remainingSize > 0 {
		return nil, fmt.Errorf("unexpected EOF - could not map full file range (missing %d bytes)", remainingSize)
	}

	return vParts, nil
}

// Part represents a segment of data available via a ReaderAt
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
		readN, readErr := part.Reader.ReadAt(p[totalRead:totalRead+int(toRead)], part.Offset+currentPartOff)
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
