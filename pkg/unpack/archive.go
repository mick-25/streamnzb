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
		if strings.HasSuffix(lower, ".rar") || isRarPart(lower) {
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
		// Scan and return new blueprint
		bp, err := ScanArchive(files)
		if err != nil {
			return nil, "", 0, nil, err
		}
		
		s, name, size, err := StreamFromBlueprint(bp)
		return s, name, size, bp, err
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
		if strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") {
			return f.OpenStream(), name, f.Size(), nil, nil
		}
	}

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
	VolFile      *loader.File
	VolOffset    int64 
}

// OpenRarStream implements the NZBDav strategy... (Legacy wrapper)
func OpenRarStream(files []*loader.File, _ string) (ReadSeekCloser, string, int64, error) {
	bp, err := ScanArchive(files)
	if err != nil {
		return nil, "", 0, err
	}
	return StreamFromBlueprint(bp)
}

func StreamFromBlueprint(bp *ArchiveBlueprint) (ReadSeekCloser, string, int64, error) {
	vs := &VirtualStream{
		totalSize: bp.TotalSize,
		dataChan:  make(chan []byte, 50),
		errChan:   make(chan error, 1),
		closeChan: make(chan struct{}),
		seekChan:  make(chan int64),
	}
	
	for _, p := range bp.Parts {
		vs.parts = append(vs.parts, virtualPart{
			VirtualStart: p.VirtualStart,
			VirtualEnd:   p.VirtualEnd,
			VolFile:      p.VolFile,
			VolOffset:    p.VolOffset,
		})
	}
	
	vs.currentPartIdx = -1
	go vs.worker()
	
	return vs, bp.MainFileName, bp.TotalSize, nil
}

func ScanArchive(files []*loader.File) (*ArchiveBlueprint, error) {
	// 1. Gather RAR files
	var rarFiles []*loader.File
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
		VolFile      *loader.File
		VolName      string
	}
	
	var mu sync.Mutex
	var parts []FilePartInfo
	
	sem := make(chan struct{}, 20) // Limit concurrency
	var wg sync.WaitGroup
	
	start := time.Now()
	
	// Filter: Only scan first volumes to avoid "bad volume number" errors
	// But keep standalone .rar files and .r00 files
	var rarFilesToScan []*loader.File
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
		go func(f *loader.File) {
			defer wg.Done()
			defer func() { <-sem }()
			
			cleanName := extractFilename(f.Name())
			singleMap := map[string]*loader.File{
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
			var foundArchives bool
			for _, p := range parts {
				lower := strings.ToLower(p.Name)
				if strings.HasSuffix(lower, ".rar") || strings.HasSuffix(lower, ".r00") || strings.HasSuffix(lower, ".zip") || isRarPart(lower) {
					foundArchives = true
					break
				}
			}
			if foundArchives {
				return nil, fmt.Errorf("nested archive detected (recursive extraction not supported)")
			}
			return nil, fmt.Errorf("no video file found in archive")
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

// virtualPart maps a range of the virtual file to a physical location
type virtualPart struct {
	VirtualStart int64
	VirtualEnd   int64
	
	VolFile      *loader.File
	VolOffset    int64 
}

type VirtualStream struct {
	parts       []virtualPart
	totalSize   int64
	
	currentOffset int64
	
	dataChan   chan []byte
	errChan    chan error
	closeChan  chan struct{}
	seekChan   chan int64
	
	currentBuf []byte
	bufOffset  int
	
	currentReader io.ReadCloser
	currentPartIdx int
	
	workerOnce sync.Once
}

func (s *VirtualStream) worker() {
	var currentOffset int64 = 0
	const chunkSize = 1024 * 1024 // 1MB chunks instead of 256KB
	
	select {
	case off := <-s.seekChan:
		currentOffset = off
	default:
	}
	
	for {
		if currentOffset >= s.totalSize {
			select {
			case s.errChan <- io.EOF:
				select {
				case <-s.closeChan: return
				case off := <-s.seekChan: currentOffset = off
				}
			case <-s.closeChan: return
			case off := <-s.seekChan: currentOffset = off
			}
			continue
		}
		
		var activePart *virtualPart
		var partIdx int
		for i := range s.parts {
			if currentOffset >= s.parts[i].VirtualStart && currentOffset < s.parts[i].VirtualEnd {
				activePart = &s.parts[i]
				partIdx = i
				break
			}
		}
		
		if activePart == nil {
			select {
			case s.errChan <- fmt.Errorf("offset %d not mapped", currentOffset):
				return
			case <-s.closeChan: return
			}
		}
		
		remaining := activePart.VirtualEnd - currentOffset
		
		// Optimize: Use cached reader if possible
		if s.currentReader == nil || s.currentPartIdx != partIdx {
			// Close old reader
			if s.currentReader != nil {
				s.currentReader.Close()
				s.currentReader = nil
			}
			
			// Open new SmartStream
			// Calculate offset within the volume file
			// currentOffset is absolute in VirtualStream
			// We need local offset in the volume file
			
			localOff := currentOffset - activePart.VirtualStart
			volOff := activePart.VolOffset + localOff
			
			s.currentReader = activePart.VolFile.OpenSmartStream(volOff)
			s.currentPartIdx = partIdx
		}
		
		// Read from stream
		readSize := int64(chunkSize)
		if readSize > remaining {
			readSize = remaining
		}
		
		buf := make([]byte, readSize)
		n, err := s.currentReader.Read(buf)
		
		if n > 0 {
			// Send data
			select {
			case s.dataChan <- buf[:n]:
			case <-s.closeChan: 
				s.currentReader.Close()
				return
			case off := <-s.seekChan:
				currentOffset = off
				s.currentReader.Close()
				s.currentReader = nil
				continue
			}
			currentOffset += int64(n)
		}
		
		if err != nil {
			if err == io.EOF {
				s.currentReader.Close()
				s.currentReader = nil
			} else {
				select {
				case s.errChan <- err:
				case <-s.closeChan: 
					s.currentReader.Close()
					return
				case off := <-s.seekChan: 
					currentOffset = off
					s.currentReader.Close()
					s.currentReader = nil
				}
			}
		}
	}
}

func (s *VirtualStream) Read(p []byte) (n int, err error) {
	if len(s.currentBuf) == 0 {
		select {
		case buf := <-s.dataChan:
			s.currentBuf = buf
			s.bufOffset = 0
		case err := <-s.errChan:
			return 0, err
		case <-s.closeChan:
			return 0, io.ErrClosedPipe
		}
	}
	
	available := len(s.currentBuf) - s.bufOffset
	toCopy := len(p)
	if available < toCopy {
		toCopy = available
	}
	
	copy(p, s.currentBuf[s.bufOffset:s.bufOffset+toCopy])
	s.bufOffset += toCopy
	s.currentOffset += int64(toCopy)
	
	if s.bufOffset >= len(s.currentBuf) {
		s.currentBuf = nil
	}
	
	return toCopy, nil
}

func (s *VirtualStream) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.currentOffset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	}
	
	if target < 0 || target > s.totalSize {
		return 0, errors.New("seek out of bounds")
	}
	
	s.currentBuf = nil
	s.bufOffset = 0
	
	select {
	case s.seekChan <- target:
	case <-s.closeChan:
		return 0, io.ErrClosedPipe
	}
	
	Loop:
	for {
		select {
		case <-s.dataChan:
		case <-s.errChan:
		default:
			break Loop
		}
	}
	
	s.currentOffset = target
	return target, nil
}

func (s *VirtualStream) Close() error {
	s.workerOnce.Do(func() {
		close(s.closeChan)
	})
	return nil
}

// InspectRAR checks a RAR archive for video content or nested archives without full scanning.
// It finds the first volume among the provided files and reads its header.
func InspectRAR(files []*loader.File) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("no files provided for inspection")
	}

	// Find the first RAR volume
	var firstVol *loader.File
	for _, f := range files {
		nameLower := strings.ToLower(f.Name())
		// Look for .rar or .part01.rar or .part1.rar
		if strings.HasSuffix(nameLower, ".rar") || strings.Contains(nameLower, ".part01.") || strings.Contains(nameLower, ".part1.") {
			firstVol = f
			break
		}
	}

	// Fallback to first file if no obvious RAR found, but might be .r00 etc.
	if firstVol == nil {
		firstVol = files[0]
	}

	stream := firstVol.OpenStream()
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
			
			// Check for nested archives (explicit failure)
			if strings.HasSuffix(name, ".rar") || 
			   strings.HasSuffix(name, ".zip") || 
			   strings.HasSuffix(name, ".7z") ||
			   IsRarPart(name) {
				return "", fmt.Errorf("nested archive detected")
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
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
