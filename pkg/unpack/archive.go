package unpack

import (
	"io"
	"strings"

	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"
)

// ReadSeekCloser combines Reader, Seeker and Closer
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// GetMediaStream attempts to find a video file inside the provided NZB files.
// cachedBP is an optional cached ArchiveBlueprint to avoid re-scanning headers.
func GetMediaStream(files []*loader.File, cachedBP interface{}) (ReadSeekCloser, string, int64, interface{}, error) {
	// 1. Identify if RAR.
	var rarFiles []*loader.File
	for _, f := range files {
		name := ExtractFilename(f.Name())
		lower := strings.ToLower(name)
		
		if strings.HasSuffix(lower, ExtPar2) {
			continue
		}
		
		if strings.HasSuffix(lower, ExtRar) || strings.Contains(lower, ".part") || IsRarPart(lower) || IsSplitArchivePart(lower) {
			rarFiles = append(rarFiles, f)
		}
	}
	
	if len(rarFiles) > 0 {
		// Check for cached blueprint
		if cachedBP != nil {
			if bp, ok := cachedBP.(*ArchiveBlueprint); ok {
				logger.Debug("Using cached RAR blueprint", "file", bp.MainFileName)
				s, name, size, err := StreamFromBlueprint(bp)
				return s, name, size, bp, err
			}
		}
		
		logger.Info("Detected RAR archive", "volumes", len(rarFiles))
		
		// Convert to UnpackableFile interface
		unpackables := make([]UnpackableFile, len(files))
		for i, f := range files {
			unpackables[i] = f
		}

		// Scan and return new blueprint
		bp, err := ScanArchive(unpackables)
		if err != nil {
			logger.Warn("ScanArchive failed, falling back to other methods", "err", err)
			// Don't return error, fallthrough to check for other files (mkv, 7z)
		} else {
			// Success
			s, name, size, err := StreamFromBlueprint(bp)
			if err != nil {
				logger.Error("StreamFromBlueprint failed", "err", err)
				return nil, "", 0, nil, err
			}
			return s, name, size, bp, err
		}
		

	}

	// 2. Identify if 7z
	for _, f := range files {
		name := ExtractFilename(f.Name())
		if strings.HasSuffix(strings.ToLower(name), Ext7z) || strings.Contains(strings.ToLower(name), ".7z.001") {
			logger.Info("Detected 7z archive", "name", name)
			
			// Check for cached blueprint
			if cachedBP != nil {
				if bp7z, ok := cachedBP.(*SevenZipBlueprint); ok {
					logger.Debug("Using cached 7z blueprint", "file", bp7z.MainFileName)
					s, n, sz, err := Open7zStreamFromBlueprint(bp7z)
					return s, n, sz, bp7z, err
				}
			}
			
			// Create blueprint on first access
			newBp, err := CreateSevenZipBlueprint(files, name)
			if err != nil {
				return nil, "", 0, nil, err
			}
			
			// Open stream from blueprint
			s, n, sz, err := Open7zStreamFromBlueprint(newBp)
			return s, n, sz, newBp, err
		}
	}

	// 3. Look for MKV/MP4 directly
	for _, f := range files {
		name := ExtractFilename(f.Name())
		
		if IsVideoFile(name) {
			stream, err := f.OpenStream()
			if err != nil {
				return nil, "", 0, nil, err
			}
			return stream, name, f.Size(), nil, nil
		}
	}

	// 4. Obfuscated / Unknown file handling
	// If we haven't found anything yet, find the largest file
	// BUT, if it's a RAR file (which implies ScanArchive failed earlier), don't treat it as a video.
	var largestFile *loader.File
	for _, f := range files {
		name := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(name, ExtRar) || strings.Contains(name, ".part") || IsRarPart(name) || IsSplitArchivePart(name) {
			continue
		}
		if strings.HasSuffix(name, ExtPar2) || strings.HasSuffix(name, ExtNzb) || strings.HasSuffix(name, ExtNfo) {
			continue
		}
		
		if largestFile == nil || f.Size() > largestFile.Size() {
			largestFile = f
		}
	}

	if largestFile != nil && largestFile.Size() > 50*1024*1024 {
		logger.Warn("No clear media found, probing largest file for magic signature", "name", largestFile.Name(), "size", largestFile.Size())
		
		unpackables := make([]UnpackableFile, len(files))
		for i, f := range files {
			unpackables[i] = f
		}
		
		logger.Info("Attempting heuristic RAR scan on unknown files")
		bp, err := ScanArchive(unpackables)
		if err == nil {
			logger.Info("Heuristic scan found RAR archive")
			s, name, size, err := StreamFromBlueprint(bp)
			if err == nil {
				return s, name, size, bp, nil
			}
		} else {
			logger.Warn("Heuristic RAR scan failed, falling back to direct stream of largest file", "err", err)
		}
		
		// Fallback: Direct Stream largest file
		extractedName := ExtractFilename(largestFile.Name())
		stream, err := largestFile.OpenStream()
		if err != nil {
			return nil, "", 0, nil, err
		}
		// Guess extension?
		// If we don't know, maybe appending .mkv helps players?
		// Or just return as is.
		return stream, extractedName, largestFile.Size(), nil, nil
	}

	logger.Warn("GetMediaStream found no suitable media", "files", len(files), "rar_candidates", len(rarFiles))
	return nil, "", 0, nil, io.EOF
}

