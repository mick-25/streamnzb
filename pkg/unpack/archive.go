package unpack

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/loader"
	"streamnzb/pkg/logger"

	"github.com/javi11/rardecode/v2"
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
		name := extractFilename(f.Name())
		lower := strings.ToLower(name)
		
		if strings.HasSuffix(lower, ".par2") {
			continue
		}
		
		if strings.HasSuffix(lower, ".rar") || strings.Contains(lower, ".part") || isRarPart(lower) {
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
		name := extractFilename(f.Name())
		if strings.HasSuffix(strings.ToLower(name), ".7z") || strings.Contains(strings.ToLower(name), ".7z.001") {
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
		name := extractFilename(f.Name())
		lower := strings.ToLower(name)
		// Debug logging handled above
		
		if strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") {
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
		name := strings.ToLower(extractFilename(f.Name()))
		if strings.HasSuffix(name, ".rar") || strings.Contains(name, ".part") || isRarPart(name) {
			continue
		}
		if strings.HasSuffix(name, ".par2") || strings.HasSuffix(name, ".nzb") || strings.HasSuffix(name, ".nfo") {
			continue
		}
		
		if largestFile == nil || f.Size() > largestFile.Size() {
			largestFile = f
		}
	}

	if largestFile != nil && largestFile.Size() > 50*1024*1024 {
		logger.Warn("No clear media found, probing largest file for magic signature", "name", largestFile.Name(), "size", largestFile.Size())
		
		// UnpackableFile interface usage if needed, but here we have loader.File
		// Just peek the first few bytes.
		// Note: DownloadSegment(ctx, 0)
		// We need a context. Use generic TODO/Background for this probe.
		// Actually, we can use OpenStream().Read() but that might buffer too much.
		// File has DownloadSegment.
		
		// Just assume it *might* be RAR if it starts with 'Rar!'
		// But ScanArchive expects to be called.
		
		// Use a lightweight probe
		// We can't easily access DownloadSegment directly from here without context if we didn't export it well,
		// but I updated DownloadSegment to take context.
		
		// Let's rely on ScanArchive to be robust?
		// No, ScanArchive expects us to give it a set.
		// If we tell ScanArchive to scan *everything* because we suspect obfuscation?
		
		// PROBE
		// We need to import context
		// Assuming context package is available or we use context.Background()
		
		// We'll treat it as a RAR scan attempt if probe succeeds.
		// Since we don't have easy context here (refactor needed?), we'll blindly send it to ScanArchive?
		// ScanArchive is expensive if it's not a rar.
		
		// Let's blindly try to stream it if it's NOT a rar.
		// Wait, if it IS a rar, streaming it directly will fail (binary garbage).
		
		// OK, we trust ScanArchive to fail fast if no RAR headers found.
		// So if we have a large unknown file, let's TRY to ScanArchive with strictly the largest file?
		// No, a RAR set might be split.
		// If obfuscated, usually they are named consistently i.e. "giberish.001", "giberish.002" or just "giberish".
		
		// Decision: If we are here, we are desperate.
		// Attempt ScanArchive on ALL files.
		// If ScanArchive returns "no archive found" (we need to distinguish this from other errors), 
		// THEN fallback to direct stream of largest file.
		
		// scanResult, err := ScanArchive(unpackables)
		// ScanArchive logs "Detected RAR archive" if successful.
		// If ScanArchive finds something, great.
		// But ScanArchive checks for signatures too.
		
		// Current ScanArchive logic relies on extension being .rar/.rXX or file starting with "Rar!".
		// Let's modify ScanArchive to filter by signature if extension is missing?
		// Actually, let's just trigger ScanArchive here.
		
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
		extractedName := extractFilename(largestFile.Name())
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

// OpenRarStream implements the NZBDav strategy:
// 1. Scan headers of ALL RAR files INDEPENDENTLY (Parallel).
// 2. Aggregate segments for the main file.
// 3. Create VirtualStream.
// ArchiveBlueprint stores the verified structure of an archive
type ArchiveBlueprint struct {
	MainFileName string
	TotalSize    int64
	Parts        []VirtualPartDef
}

type VirtualPartDef struct {
	VirtualStart int64
	VirtualEnd   int64
	VolFile      UnpackableFile
	VolOffset    int64 
}

// OpenRarStream implements the NZBDav strategy... (Legacy wrapper)
func OpenRarStream(files []*loader.File, _ string) (io.ReadSeekCloser, string, int64, error) {
	// Convert to UnpackableFile interface
	unpackables := make([]UnpackableFile, len(files))
	for i, f := range files {
		unpackables[i] = f
	}

	bp, err := ScanArchive(unpackables)
	if err != nil {
		return nil, "", 0, err
	}
	return StreamFromBlueprint(bp)
}

func StreamFromBlueprint(bp *ArchiveBlueprint) (io.ReadSeekCloser, string, int64, error) {
	var parts []virtualPart
	for _, p := range bp.Parts {
		parts = append(parts, virtualPart{
			VirtualStart: p.VirtualStart,
			VirtualEnd:   p.VirtualEnd,
			VolFile:      p.VolFile,
			VolOffset:    p.VolOffset,
		})
	}
	
	vs := NewVirtualStream(parts, bp.TotalSize, 0)
	return vs, bp.MainFileName, bp.TotalSize, nil
}

func ScanArchive(files []UnpackableFile) (*ArchiveBlueprint, error) {
	// 1. Gather RAR files
	var rarFiles []UnpackableFile
	for _, f := range files {
		name := extractFilename(f.Name())
		lower := strings.ToLower(name)
		// Specifically exclude .par2 files which might contain ".part" in their name
		if strings.HasSuffix(lower, ".par2") {
			continue
		}
		if strings.HasSuffix(lower, ".rar") || strings.Contains(lower, ".part") || isRarPart(lower) {
			rarFiles = append(rarFiles, f)
		}
	}
	
	// 2. Scan Headers in Parallel
	type FilePartInfo struct {
		Name         string
		IsMain       bool
		UnpackedSize int64
		DataOffset   int64
		PackedSize   int64
		VolFile      UnpackableFile
		VolName      string
	}
	
	var mu sync.Mutex
	var parts []FilePartInfo
	
	sem := make(chan struct{}, 20) // Limit concurrency
	var wg sync.WaitGroup
	
	start := time.Now()
	
	// Filter: Only scan first volumes to avoid "bad volume number" errors
	// But keep standalone .rar files and .r00 files
	var rarFilesToScan []UnpackableFile
	for _, f := range rarFiles {
		name := strings.ToLower(extractFilename(f.Name()))
		
		// Always include standalone .rar files (no part number)
		if strings.HasSuffix(name, ".rar") && !strings.Contains(name, ".part") && !strings.Contains(name, ".r0") {
			rarFilesToScan = append(rarFilesToScan, f)
			continue
		}
		
		// Skip middle volumes (.part02.rar, .part03.rar, .r01, .r02, etc.)
		if isMiddleRarVolume(name) {
			continue
		}
		
		rarFilesToScan = append(rarFilesToScan, f)
	}
	
	logger.Debug("Scanning RAR first volumes", "count", len(rarFilesToScan), "total", len(rarFiles))
	
	for _, f := range rarFilesToScan {
		wg.Add(1)
		sem <- struct{}{}
		go func(f UnpackableFile) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic in ScanArchive worker", "file", f.Name(), "recover", r)
				}
			}()
			
			cleanName := extractFilename(f.Name())
			singleMap := map[string]UnpackableFile{
				cleanName: f,
			}
			fsys := NewNZBFSFromMap(singleMap)
			
			infos, err := rardecode.ListArchiveInfo(cleanName, 
				rardecode.FileSystem(fsys),
				rardecode.ParallelRead(true),
			)
			
			if err != nil {
				logger.Debug("Scan item failure", "name", cleanName, "err", err, "infos", len(infos))
			} else if len(infos) > 0 {
				logger.Debug("Scan item success", "name", cleanName, "infos", len(infos))
			} else {
				logger.Debug("Scan item - no infos", "name", cleanName)
			}

			if len(infos) > 0 {
				for _, info := range infos {
					if info.Name == "" { continue } 
					logger.Debug("Found file in archive", "clean_name", cleanName, "name", info.Name, "unpacked_size", info.TotalUnpackedSize)

					for _, p := range info.Parts {
						mu.Lock()
						parts = append(parts, FilePartInfo{
							Name:       info.Name,
							IsMain:     isMainMedia(info),
							UnpackedSize: info.TotalUnpackedSize,
							DataOffset: p.DataOffset,
							PackedSize: p.PackedSize,
							VolFile:    f,
							VolName:    f.Name(),
						})
						mu.Unlock()
					}
				}
			}
		}(f)
	}
	
	wg.Wait()
	logger.Info("Scan complete", "files", len(rarFiles), "duration", time.Since(start))
	
	// 3. Aggregate Parts
	fileCounts := make(map[string]int64)
	for _, p := range parts {
		if p.IsMain {
			fileCounts[p.Name] += p.PackedSize
		}
	}
	
	var bestName string
	var maxBytes int64
	for name, b := range fileCounts {
		if b > maxBytes {
			maxBytes = b
			bestName = name
		}
	}
	
	if bestName == "" {
		if len(parts) > 0 {
			// No direct media found, check for nested archives
			var nestedArchives []string
			for _, p := range parts {
				lower := strings.ToLower(p.Name)
				if strings.HasSuffix(lower, ".rar") || strings.HasSuffix(lower, ".r00") || strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".iso") || isRarPart(lower) {
					nestedArchives = append(nestedArchives, p.Name)
				}
			}
			
			if len(nestedArchives) > 0 {
				logger.Info("No video found, checking for nested archives", "candidates", len(nestedArchives))
				
				// Identify archive sets
				// Map: CleanName -> TotalSize
				archiveSets := make(map[string]int64)
				// Map: CleanName -> []FilePartInfo
				archivePartsMap := make(map[string][]FilePartInfo)
				
				for _, p := range parts {
					lower := strings.ToLower(p.Name)
					if strings.HasSuffix(lower, ".rar") || strings.HasSuffix(lower, ".r00") || strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".iso") || isRarPart(lower) {
						// Normalize name to identify the set
						// remove trailing .rar, .rXX, .partXX.rar
						cleanSet := p.Name
						if idx := strings.LastIndex(lower, ".part"); idx != -1 {
							cleanSet = p.Name[:idx] // "movie.part01.rar" -> "movie"
						} else if idx := strings.LastIndex(lower, ".r"); idx != -1 && idx > len(lower)-5 {
							cleanSet = p.Name[:idx] // "movie.r04" -> "movie"
						} else if strings.HasSuffix(lower, ".rar") {
							cleanSet = strings.TrimSuffix(p.Name, ".rar")
							cleanSet = strings.TrimSuffix(cleanSet, ".RAR")
						}
						
						archiveSets[cleanSet] += p.PackedSize
						archivePartsMap[cleanSet] = append(archivePartsMap[cleanSet], p)
					}
				}
				
				var bestSet string
				var maxSetBytes int64
				for set, b := range archiveSets {
					if b > maxSetBytes {
						maxSetBytes = b
						bestSet = set
					}
				}
				
				if bestSet != "" {
					logger.Info("Detected nested archive set", "set", bestSet, "size", maxSetBytes, "volumes", len(archivePartsMap[bestSet]))
					
					// Get all parts for this set
					nestedParts := archivePartsMap[bestSet]
					
					// Group by individual volume file (VirtualFile needs 1:1 map to inner files)
					// We need to create ONE VirtualFile per Volume in the nested set.
					// e.g. nested.rar -> VirtualFile(nested.rar)
					//      nested.r00 -> VirtualFile(nested.r00)
					
					// Wait, p.Name is the name INSIDE the parent archive.
					// If parent has "nested.rar", "nested.r00" inside it.
					// `parts` contains these as distinct entries.
					// We need to construct a []UnpackableFile where each entry corresponds to one of these inner files.
					
					var nestedFiles []UnpackableFile
					
					// Aggregate parts by *Name* (distinct volume files)
					// (A single inner RAR file might be split across multiple parent RAR volumes? 
					//  Yes, but Reader provides it as one stream if we model it right.
					//  Wait, `info.Name` derived parts: "nested.rar" might be split.
					//  We need to re-assemble each inner file as a VirtualFile.)
					
					// Group parts by Name (exact filename)
					innerFileParts := make(map[string][]FilePartInfo)
					for _, p := range nestedParts {
						innerFileParts[p.Name] = append(innerFileParts[p.Name], p)
					}
					
					for name, fileParts := range innerFileParts {
						sort.Slice(fileParts, func(i, j int) bool {
							return compareVolumeNames(fileParts[i].VolName, fileParts[j].VolName)
						})
						
						var vfParts []virtualPart
						var vOffset int64 = 0
						for _, p := range fileParts {
							vfParts = append(vfParts, virtualPart{
								VirtualStart: vOffset,
								VirtualEnd:   vOffset + p.PackedSize,
								VolFile:      p.VolFile,
								VolOffset:    p.DataOffset,
							})
							vOffset += p.PackedSize
						}
						
						// Size: Use UnpackedSize from first part if available
						totalSize := fileParts[0].UnpackedSize
						if totalSize == 0 { totalSize = vOffset } // Fallback
						
						vf := NewVirtualFile(name, totalSize, vfParts)
						nestedFiles = append(nestedFiles, vf)
					}
					
					// Recurse with ALL volumes!
					logger.Info("Recursively scanning nested archive set", "set", bestSet, "volumes", len(nestedFiles))
					return ScanArchive(nestedFiles)
				}
			}
			return nil, fmt.Errorf("no video or nested archive found")
		}
		return nil, errors.New("timeout waiting for workers or empty archive")
	}

	logger.Info("Selected Main Media", "name", bestName, "size", maxBytes)
	
	var mainParts []FilePartInfo
	for _, p := range parts {
		if p.Name == bestName {
			mainParts = append(mainParts, p)
		}
	}
	
	sort.Slice(mainParts, func(i, j int) bool {
		return compareVolumeNames(mainParts[i].VolName, mainParts[j].VolName)
	})
	
	totalHeaderSize := mainParts[0].UnpackedSize
	if maxBytes < totalHeaderSize && len(rarFiles) > len(mainParts) {
		logger.Debug("Size mismatch - attempting manual volume aggregation", "header", totalHeaderSize, "scanned", maxBytes)
		
		sort.Slice(rarFiles, func(i, j int) bool {
			return compareVolumeNames(rarFiles[i].Name(), rarFiles[j].Name())
		})
		
		startIdx := -1
		startVolName := mainParts[0].VolName
		for i, f := range rarFiles {
			if f.Name() == startVolName {
				startIdx = i
				break
			}
		}
		
		if startIdx != -1 {
			firstPart := mainParts[0] 
			mainParts = []FilePartInfo{firstPart}
			
			for i := startIdx + 1; i < len(rarFiles); i++ {
				f := rarFiles[i]
				blindPart := FilePartInfo{
					Name:       bestName,
					IsMain:     true,
					UnpackedSize: totalHeaderSize, 
					DataOffset: firstPart.DataOffset, 
					PackedSize: f.Size() - firstPart.DataOffset, 
					VolFile:    f,
					VolName:    f.Name(),
				}
				
				if blindPart.PackedSize > 0 {
					mainParts = append(mainParts, blindPart)
				}
			}
			logger.Debug("Manual aggregation complete", "added", len(mainParts)-1, "total", len(mainParts))
		}
	}

	bp := &ArchiveBlueprint{
		MainFileName: bestName,
		TotalSize:    totalHeaderSize,
	}
	
	var vOffset int64 = 0
	for _, p := range mainParts {
		bp.Parts = append(bp.Parts, VirtualPartDef{
			VirtualStart: vOffset,
			VirtualEnd:   vOffset + p.PackedSize,
			VolFile:      p.VolFile,
			VolOffset:    p.DataOffset,
		})
		vOffset += p.PackedSize
	}
	
	if vOffset < totalHeaderSize {
		logger.Debug("Adjusting stream size from header", "header", totalHeaderSize, "actual", vOffset)
		bp.TotalSize = vOffset
	}

	return bp, nil
}

func compareVolumeNames(n1, n2 string) bool {
	// Simple string compare often works for part01, part02
	// But .r1 vs .r10 vs .r2 might fail standard string sort?
	// Actually string sort handles part01 vs part02 fine.
	// But r1 vs r10 is tricky.
	// Use simple string comparison for now, assuming standard naming.
	return extractFilename(n1) < extractFilename(n2)
}

func isMainMedia(info rardecode.ArchiveFileInfo) bool {
	name := info.Name
	lower := strings.ToLower(name)
	
	// Explicitly check for video extensions
	isVideo := strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") || strings.HasSuffix(lower, ".iso")
	
	// Check if large enough to be media
	isLarge := info.TotalUnpackedSize > 50*1024*1024
	
	// Exclude archive/parity files even if large (prevents nested archive streaming)
	isArchive := strings.HasSuffix(lower, ".rar") || strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".7z") || strings.HasSuffix(lower, ".par2") || isRarPart(lower)
	
	return isVideo || (isLarge && !isArchive)
}



// InspectRAR checks a RAR archive for video content or nested archives without full scanning.
// It finds the first volume among the provided files and reads its header.
func InspectRAR(files []*loader.File) (filename string, err error) {
	if len(files) == 0 {
		return "", fmt.Errorf("no files provided for inspection")
	}

	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in InspectRAR", "recover", r)
			err = fmt.Errorf("panic in InspectRAR: %v", r)
		}
	}()

	// Find the first RAR volume
	var firstVol *loader.File
	
	// First pass: Look for definite first volumes
	for _, f := range files {
		nameLower := strings.ToLower(f.Name())
		// Explicitly skip PAR2 and other non-archive files
		if strings.HasSuffix(nameLower, ".par2") || strings.HasSuffix(nameLower, ".nzb") || strings.HasSuffix(nameLower, ".nfo") {
			continue
		}

		// Look for .rar or .part01.rar or .part1.rar
		if (strings.HasSuffix(nameLower, ".rar") && !strings.Contains(nameLower, ".part")) ||
		   strings.Contains(nameLower, ".part01.") || 
		   strings.Contains(nameLower, ".part1.") ||
		   strings.HasSuffix(nameLower, ".r00") ||
		   strings.HasSuffix(nameLower, ".001") {
			firstVol = f
			break
		}
	}

	// Second pass: Just look for any .rar if first pass failed
	if firstVol == nil {
		for _, f := range files {
			nameLower := strings.ToLower(f.Name())
			if strings.HasSuffix(nameLower, ".par2") || strings.HasSuffix(nameLower, ".nzb") || strings.HasSuffix(nameLower, ".nfo") {
				continue
			}
			if strings.HasSuffix(nameLower, ".rar") {
				firstVol = f
				break
			}
		}
	}

	if firstVol == nil {
		return "", fmt.Errorf("no valid RAR volume found for inspection")
	}

	stream, err := firstVol.OpenStream()
	if err != nil {
		return "", fmt.Errorf("failed to open stream for inspection: %w", err)
	}
	defer stream.Close()

	// rardecode.NewReader works for streaming (single volume or start of split)
	r, err := rardecode.NewReader(stream)
	if err != nil {
		return "", fmt.Errorf("failed to open rar %s: %w", firstVol.Name(), err)
	}

	// Scan first few headers
	// We limit scanning to avoid reading too much if many small files
	maxFiles := 50
	
	for i := 0; i < maxFiles; i++ {
		header, err := r.Next()
		
		// Check header content even if error occurred (e.g. multi-volume warning)
		if header != nil && !header.IsDir {
			// Check extensions
			name := strings.ToLower(header.Name)
			
			// Check for video
			if strings.HasSuffix(name, ".mkv") || 
			   strings.HasSuffix(name, ".mp4") || 
			   strings.HasSuffix(name, ".avi") ||
			   strings.HasSuffix(name, ".iso") {
				return header.Name, nil
			}
			
			// Nested archives are now supported, so we don't return an error here.
			// If we don't find a video, ScanArchive will pick up the nested archive later.
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			// If we hit "multi-volume archive continues in next file", it just means 
			// we reached the end of the first volume headers.
			if strings.Contains(err.Error(), "multi-volume archive") {
				break
			}
			
			// If we hit an error (e.g. need next volume), but haven't found video yet,
			// check if the error is just "need volume".
			return "", err
		}
	}

	return "", fmt.Errorf("no video found in rar")
}

// IsRarPart checks if extension is .rXX
func IsRarPart(name string) bool {
	// Simple check for .r[0-9][0-9] suffix
	if len(name) < 4 {
		return false
	}

	// Check last 4 chars: .rNN
	ext := name[len(name)-4:]
	if ext[0] != '.' || ext[1] != 'r' {
		return false
	}

	// Check digits
	return isDigit(ext[2]) && isDigit(ext[3])
}

func isRarPart(name string) bool {
	return IsRarPart(name)
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// isMiddleRarVolume checks if a RAR file is a middle volume (not the first)
func isMiddleRarVolume(name string) bool {
	name = strings.ToLower(name)
	
	// Match .partXX.rar format
	if strings.Contains(name, ".part") && strings.HasSuffix(name, ".rar") {
		// First volumes: .part1.rar, .part01.rar, .part001.rar
		if strings.Contains(name, ".part1.rar") || 
		   strings.Contains(name, ".part01.rar") || 
		   strings.Contains(name, ".part001.rar") {
			return false
		}
		// Any other .partXX.rar is a middle volume
		return true
	}
	
	// Match .r01, .r02, etc. (but not .r00 or .rar)
	if len(name) >= 4 && name[len(name)-4:len(name)-2] == ".r" {
		lastTwo := name[len(name)-2:]
		// .r00 is first volume, .r01+ are middle volumes
		if lastTwo != "ar" && lastTwo != "00" {
			return true
		}
	}
	
	return false
}
