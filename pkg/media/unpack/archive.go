package unpack

import (
	"context"
	"io"
	"strings"

	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/core/logger"
)

// ReadSeekCloser combines Reader, Seeker and Closer.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// DirectBlueprint caches the result of direct/obfuscated file detection
// so subsequent play requests skip the full detection pipeline.
type DirectBlueprint struct {
	FileName  string
	FileIndex int
}

// GetMediaStream finds a video file inside the provided NZB files and returns
// a seekable stream. ctx controls the lifetime of the returned stream.
// cachedBP is an optional cached blueprint to avoid re-scanning headers.
func GetMediaStream(ctx context.Context, files []*loader.File, cachedBP interface{}) (ReadSeekCloser, string, int64, interface{}, error) {
	// Fast path: use cached blueprint
	if cachedBP != nil {
		switch bp := cachedBP.(type) {
		case *ArchiveBlueprint:
			logger.Debug("Using cached RAR blueprint", "file", bp.MainFileName)
			s, name, size, err := StreamFromBlueprint(ctx, bp)
			return s, name, size, bp, err
		case *SevenZipBlueprint:
			logger.Debug("Using cached 7z blueprint", "file", bp.MainFileName)
			s, n, sz, err := Open7zStreamFromBlueprint(ctx, bp)
			return s, n, sz, bp, err
		case *DirectBlueprint:
			if bp.FileIndex < len(files) {
				f := files[bp.FileIndex]
				stream, err := f.OpenStreamCtx(ctx)
				if err != nil {
					return nil, "", 0, nil, err
				}
				return stream, bp.FileName, f.Size(), bp, nil
			}
		}
	}

	// 1. RAR detection
	var rarFiles []*loader.File
	for _, f := range files {
		name := ExtractFilename(f.Name())
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ExtPar2) || strings.Contains(lower, ".7z.") {
			continue
		}
		if strings.HasSuffix(lower, ExtRar) || strings.Contains(lower, ".part") || IsRarPart(lower) || IsSplitArchivePart(lower) {
			rarFiles = append(rarFiles, f)
		}
	}

	if len(rarFiles) > 0 {
		logger.Info("Detected RAR archive", "volumes", len(rarFiles))
		unpackables := make([]UnpackableFile, len(files))
		for i, f := range files {
			unpackables[i] = f
		}
		bp, err := ScanArchive(unpackables)
		if err != nil {
			logger.Warn("ScanArchive failed, falling back to other methods", "err", err)
		} else {
			s, name, size, err := StreamFromBlueprint(ctx, bp)
			if err != nil {
				return nil, "", 0, nil, err
			}
			return s, name, size, bp, nil
		}
	}

	// 2. 7z detection
	for _, f := range files {
		name := ExtractFilename(f.Name())
		if strings.HasSuffix(strings.ToLower(name), Ext7z) || strings.Contains(strings.ToLower(name), ".7z.001") {
			logger.Info("Detected 7z archive", "name", name)
			newBp, err := CreateSevenZipBlueprint(files, name)
			if err != nil {
				return nil, "", 0, nil, err
			}
			s, n, sz, err := Open7zStreamFromBlueprint(ctx, newBp)
			return s, n, sz, newBp, err
		}
	}

	// 3. Direct video files
	for i, f := range files {
		name := ExtractFilename(f.Name())
		if IsVideoFile(name) {
			stream, err := f.OpenStreamCtx(ctx)
			if err != nil {
				return nil, "", 0, nil, err
			}
			bp := &DirectBlueprint{FileName: name, FileIndex: i}
			return stream, name, f.Size(), bp, nil
		}
	}

	// 4. Obfuscated / unknown: find largest non-archive file
	var largestFile *loader.File
	var largestIdx int
	for i, f := range files {
		name := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(name, ExtRar) || strings.Contains(name, ".part") || IsRarPart(name) || IsSplitArchivePart(name) {
			continue
		}
		if strings.HasSuffix(name, ExtPar2) || strings.HasSuffix(name, ExtNzb) || strings.HasSuffix(name, ExtNfo) {
			continue
		}
		if largestFile == nil || f.Size() > largestFile.Size() {
			largestFile = f
			largestIdx = i
		}
	}

	if largestFile != nil && largestFile.Size() > 50*1024*1024 {
		logger.Warn("No clear media found, probing largest file", "name", largestFile.Name(), "size", largestFile.Size())

		unpackables := make([]UnpackableFile, len(files))
		for i, f := range files {
			unpackables[i] = f
		}

		logger.Info("Attempting heuristic RAR scan on unknown files")
		bp, err := ScanArchive(unpackables)
		if err == nil {
			logger.Info("Heuristic scan found RAR archive")
			s, name, size, err := StreamFromBlueprint(ctx, bp)
			if err == nil {
				return s, name, size, bp, nil
			}
		} else {
			logger.Warn("Heuristic RAR scan failed, falling back to direct stream", "err", err)
		}

		extractedName := ExtractFilename(largestFile.Name())
		stream, err := largestFile.OpenStreamCtx(ctx)
		if err != nil {
			return nil, "", 0, nil, err
		}
		directBP := &DirectBlueprint{FileName: extractedName, FileIndex: largestIdx}
		return stream, extractedName, largestFile.Size(), directBP, nil
	}

	logger.Warn("GetMediaStream found no suitable media", "files", len(files), "rar_candidates", len(rarFiles))
	return nil, "", 0, nil, io.EOF
}
