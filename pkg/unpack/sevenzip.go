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
	FileOffset   int64 // Offset of the file data within the archive
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
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") {
			// Check if file is compressed
			if fi.Compressed {
				logger.Debug("Skipping compressed file in 7z", "name", filepath.Base(name))
				continue
			}

			logger.Debug("Found uncompressed video file in 7z", "name", filepath.Base(name), "offset", fi.Offset, "size", fi.Size)

			// Create a reader that maps the file's position to segments
			stream := &Offset7zStream{
				parts:      parts,
				fileOffset: int64(fi.Offset),
				fileSize:   int64(fi.Size),
				currentPos: 0,
			}

			return stream, filepath.Base(name), int64(fi.Size), nil
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
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") {
			if fi.Compressed {
				continue // Skip compressed files
			}

			// Scoring logic:
			// 1. Prefer non-sample files
			// 2. Prefer larger files

			if bestIdx == -1 {
				bestIdx = i
				bestSize = int64(fi.Size)
				continue
			}

			isSample := strings.Contains(lower, "sample")
			bestIsSample := strings.Contains(strings.ToLower(fileInfos[bestIdx].Name), "sample")

			if !isSample && bestIsSample {
				// Found a real file, replace sample
				bestIdx = i
				bestSize = int64(fi.Size)
			} else if isSample && !bestIsSample {
				// Found sample but we have a real file, ignore sample
				continue
			} else {
				// Both are samples or both are real files, pick largest
				if int64(fi.Size) > bestSize {
					bestIdx = i
					bestSize = int64(fi.Size)
				}
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

	stream := &Offset7zStream{
		parts:      parts,
		fileOffset: int64(bp.FileOffset),
		fileSize:   bp.TotalSize,
		currentPos: 0,
	}

	return stream, bp.MainFileName, bp.TotalSize, nil
}

// Offset7zStream streams data from an uncompressed 7z archive by offset
type Offset7zStream struct {
	parts      []Part
	fileOffset int64 // Where the file data starts in the archive
	fileSize   int64 // Size of the file
	currentPos int64 // Current read position within the file
}

func (s *Offset7zStream) Read(p []byte) (int, error) {
	if s.currentPos >= s.fileSize {
		return 0, io.EOF
	}

	// Calculate absolute position in archive
	absPos := s.fileOffset + s.currentPos

	// Find which part contains this position
	var partOffset int64
	var currentPart *Part
	for i := range s.parts {
		if absPos >= partOffset && absPos < partOffset+s.parts[i].Size {
			currentPart = &s.parts[i]
			break
		}
		partOffset += s.parts[i].Size
	}

	if currentPart == nil {
		return 0, io.EOF
	}

	// Read from the part
	relativeOffset := absPos - partOffset
	toRead := int64(len(p))
	if toRead > s.fileSize-s.currentPos {
		toRead = s.fileSize - s.currentPos
	}
	if toRead > currentPart.Size-relativeOffset {
		toRead = currentPart.Size - relativeOffset
	}

	n, err := currentPart.Reader.ReadAt(p[:toRead], relativeOffset)
	s.currentPos += int64(n)

	if err != nil && err != io.EOF {
		return n, err
	}

	if s.currentPos >= s.fileSize {
		return n, io.EOF
	}

	return n, nil
}

func (s *Offset7zStream) Seek(offset int64, whence int) (int64, error) {
	var newPos int64

	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = s.currentPos + offset
	case io.SeekEnd:
		newPos = s.fileSize + offset
	}

	if newPos < 0 {
		return 0, errors.New("invalid seek position")
	}

	s.currentPos = newPos
	return newPos, nil
}

func (s *Offset7zStream) Close() error {
	return nil
}
