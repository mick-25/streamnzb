package unpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/loader"

	"github.com/javi11/rardecode/v2"
)

// ArchiveBlueprint stores the verified structure of a RAR archive.
type ArchiveBlueprint struct {
	MainFileName string
	TotalSize    int64
	Parts        []VirtualPartDef
	IsCompressed bool
}

type VirtualPartDef struct {
	VirtualStart int64
	VirtualEnd   int64
	VolFile      UnpackableFile
	VolOffset    int64
}

func StreamFromBlueprint(ctx context.Context, bp *ArchiveBlueprint) (io.ReadSeekCloser, string, int64, error) {
	if bp.IsCompressed {
		return nil, "", 0, fmt.Errorf("compressed RAR archive (file: %s) -- STORE mode required for streaming", bp.MainFileName)
	}

	parts := make([]virtualPart, len(bp.Parts))
	for i, p := range bp.Parts {
		parts[i] = virtualPart(p)
	}
	return NewVirtualStream(ctx, parts, bp.TotalSize, 0), bp.MainFileName, bp.TotalSize, nil
}

// ScanArchive scans RAR volumes in parallel to build a blueprint.
func ScanArchive(files []UnpackableFile) (*ArchiveBlueprint, error) {
	rarFiles := filterRarFiles(files)
	if len(rarFiles) == 0 {
		return nil, errors.New("no RAR files found")
	}

	// Only scan first volumes to avoid "bad volume number" errors
	firstVols := filterFirstVolumes(rarFiles)
	logger.Debug("Scanning RAR first volumes", "count", len(firstVols), "total", len(rarFiles))

	start := time.Now()
	parts := scanVolumesParallel(firstVols)
	logger.Info("RAR scan complete", "files", len(rarFiles), "duration", time.Since(start))

	// Fail fast on compression
	for _, p := range parts {
		if p.isCompressed {
			return nil, fmt.Errorf("compressed RAR archive (file: %s) -- STORE mode required for streaming", p.name)
		}
	}

	bp, err := buildBlueprint(parts, rarFiles)
	if err != nil {
		return nil, err
	}
	return bp, nil
}

// InspectRAR reads the first volume's headers to check for video content.
func InspectRAR(files []*loader.File) (string, error) {
	if len(files) == 0 {
		return "", errors.New("no files provided")
	}

	firstVol := findFirstVolume(files)
	if firstVol == nil {
		return "", errors.New("no valid RAR volume found")
	}

	stream, err := firstVol.OpenStream()
	if err != nil {
		return "", fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	r, err := rardecode.NewReader(stream)
	if err != nil {
		return "", fmt.Errorf("failed to open rar: %w", err)
	}

	for i := 0; i < 50; i++ {
		header, err := r.Next()
		if header != nil && !header.IsDir && IsVideoFile(header.Name) {
			return header.Name, nil
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if strings.Contains(err.Error(), "multi-volume archive") {
				break
			}
			return "", err
		}
	}
	return "", errors.New("no video found in rar")
}

// --- internal types ---

type filePart struct {
	name         string
	unpackedSize int64
	dataOffset   int64
	packedSize   int64
	volFile      UnpackableFile
	volName      string
	isMedia      bool
	isCompressed bool
}

// --- scanning ---

func scanVolumesParallel(files []UnpackableFile) []filePart {
	var mu sync.Mutex
	var result []filePart
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(f UnpackableFile) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic scanning RAR", "file", f.Name(), "err", r)
				}
			}()

			cleanName := ExtractFilename(f.Name())
			fsys := NewNZBFSFromMap(map[string]UnpackableFile{cleanName: f})

			infos, err := rardecode.ListArchiveInfo(cleanName,
				rardecode.FileSystem(fsys),
				rardecode.ParallelRead(true),
			)
			if err != nil {
				logger.Debug("Scan failure", "name", cleanName, "err", err)
			}

			for _, info := range infos {
				if info.Name == "" {
					continue
				}
				logger.Debug("Found file in archive", "vol", cleanName, "name", info.Name, "size", info.TotalUnpackedSize)

				compressed := false
				for _, p := range info.Parts {
					if p.CompressionMethod != "stored" {
						compressed = true
					}
				}

				for _, p := range info.Parts {
					mu.Lock()
					result = append(result, filePart{
						name:         info.Name,
						unpackedSize: info.TotalUnpackedSize,
						dataOffset:   p.DataOffset,
						packedSize:   p.PackedSize,
						volFile:      f,
						volName:      f.Name(),
						isMedia:      isMediaFile(info),
						isCompressed: compressed,
					})
					mu.Unlock()
				}
			}
		}(f)
	}
	wg.Wait()
	return result
}

// --- blueprint construction ---

func buildBlueprint(parts []filePart, allRarFiles []UnpackableFile) (*ArchiveBlueprint, error) {
	bestName := selectMainFile(parts)
	if bestName == "" {
		return tryNestedArchive(parts)
	}

	logger.Info("Selected main media", "name", bestName)

	mainParts := collectParts(parts, bestName)
	sortByVolume(mainParts)

	headerSize := mainParts[0].unpackedSize
	scannedSize := totalPackedSize(mainParts)

	// If scanned parts don't cover the full file, fill from remaining volumes
	if scannedSize < headerSize && len(allRarFiles) > len(mainParts) {
		mainParts = aggregateRemainingVolumes(mainParts, allRarFiles, bestName, headerSize)
	}

	compressed := false
	for _, p := range mainParts {
		if p.isCompressed {
			compressed = true
			break
		}
	}

	bp := &ArchiveBlueprint{
		MainFileName: bestName,
		TotalSize:    headerSize,
		IsCompressed: compressed,
	}

	var vOffset int64
	for _, p := range mainParts {
		bp.Parts = append(bp.Parts, VirtualPartDef{
			VirtualStart: vOffset,
			VirtualEnd:   vOffset + p.packedSize,
			VolFile:      p.volFile,
			VolOffset:    p.dataOffset,
		})
		vOffset += p.packedSize
	}

	if vOffset < headerSize {
		logger.Debug("Adjusting stream size", "header", headerSize, "actual", vOffset)
		bp.TotalSize = vOffset
	}

	// Pre-warm the last volume's final segment. Video players always seek to
	// the end of the file to read MKV/MP4 metadata (Cues / moov atom) before
	// they can start playback. Kicking this download off now means the data
	// is likely cached by the time the first play request arrives.
	lastPart := mainParts[len(mainParts)-1]
	if pw, ok := lastPart.volFile.(segmentPrewarmer); ok {
		pw.PrewarmSegment(pw.SegmentCount() - 1)
	}

	return bp, nil
}

func selectMainFile(parts []filePart) string {
	sizes := make(map[string]int64)
	for _, p := range parts {
		if p.isMedia {
			sizes[p.name] += p.packedSize
		}
	}
	var best string
	var maxSize int64
	for name, sz := range sizes {
		if sz > maxSize {
			maxSize = sz
			best = name
		}
	}
	return best
}

func collectParts(parts []filePart, name string) []filePart {
	var result []filePart
	for _, p := range parts {
		if p.name == name {
			result = append(result, p)
		}
	}
	return result
}

func totalPackedSize(parts []filePart) int64 {
	var total int64
	for _, p := range parts {
		total += p.packedSize
	}
	return total
}

// segmentMapper is satisfied by loader.File to trigger decoded size detection.
type segmentMapper interface {
	EnsureSegmentMap() error
}

// segmentPrewarmer is satisfied by loader.File to pre-download segments
// in the background so data is cached before the player requests it.
type segmentPrewarmer interface {
	segmentMapper
	SegmentCount() int
	PrewarmSegment(index int)
}

func aggregateRemainingVolumes(mainParts []filePart, allRarFiles []UnpackableFile, name string, headerSize int64) []filePart {
	sort.Slice(allRarFiles, func(i, j int) bool {
		return volumeOrder(allRarFiles[i].Name()) < volumeOrder(allRarFiles[j].Name())
	})

	startVol := mainParts[0].volName
	startIdx := -1
	for i, f := range allRarFiles {
		if f.Name() == startVol {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return mainParts
	}

	// Probe the first continuation volume to determine the RAR header
	// overhead. Each continuation volume has a signature + archive header +
	// file continuation header before the actual data. Without this, the
	// VirtualStream reads RAR header bytes as media data, corrupting the
	// stream at every volume boundary.
	contDataOffset := probeContinuationOffset(allRarFiles, startIdx, name)
	if contDataOffset > 0 {
		logger.Debug("Probed continuation volume header", "dataOffset", contDataOffset)
	}

	first := mainParts[0]
	result := []filePart{first}

	for i := startIdx + 1; i < len(allRarFiles); i++ {
		f := allRarFiles[i]
		if sm, ok := f.(segmentMapper); ok {
			sm.EnsureSegmentMap()
		}
		if f.Size() <= 0 {
			continue
		}
		dataSize := f.Size() - contDataOffset
		if dataSize <= 0 {
			continue
		}
		result = append(result, filePart{
			name:         name,
			unpackedSize: headerSize,
			dataOffset:   contDataOffset,
			packedSize:   dataSize,
			volFile:      f,
			volName:      f.Name(),
			isMedia:      true,
		})
	}
	logger.Debug("Manual volume aggregation", "added", len(result)-1, "total", len(result))
	return result
}

func probeContinuationOffset(allRarFiles []UnpackableFile, startIdx int, targetName string) int64 {
	if startIdx+1 >= len(allRarFiles) {
		return 0
	}

	firstFile := allRarFiles[startIdx]
	secondFile := allRarFiles[startIdx+1]
	firstName := ExtractFilename(firstFile.Name())
	secondName := ExtractFilename(secondFile.Name())

	fsys := NewNZBFSFromMap(map[string]UnpackableFile{
		firstName:  firstFile,
		secondName: secondFile,
	})

	infos, err := rardecode.ListArchiveInfo(firstName,
		rardecode.FileSystem(fsys),
		rardecode.ParallelRead(true),
	)
	if err != nil {
		logger.Debug("Continuation probe failed, falling back to zero offset", "err", err)
		return 0
	}

	lowerTarget := strings.ToLower(targetName)
	for _, info := range infos {
		if strings.ToLower(info.Name) != lowerTarget {
			continue
		}
		if len(info.Parts) >= 2 {
			return info.Parts[1].DataOffset
		}
	}
	return 0
}

// --- nested archive handling ---

func tryNestedArchive(parts []filePart) (*ArchiveBlueprint, error) {
	if len(parts) == 0 {
		return nil, errors.New("empty archive")
	}

	// Group archive files by set name
	type archiveSet struct {
		totalSize int64
		parts     []filePart
	}
	sets := make(map[string]*archiveSet)

	for _, p := range parts {
		if !IsArchiveFile(p.name) {
			continue
		}
		setName := archiveSetName(p.name)
		s, ok := sets[setName]
		if !ok {
			s = &archiveSet{}
			sets[setName] = s
		}
		s.totalSize += p.packedSize
		s.parts = append(s.parts, p)
	}

	if len(sets) == 0 {
		return nil, errors.New("no video or nested archive found")
	}

	// Pick largest set
	var bestSet string
	var maxSize int64
	for name, s := range sets {
		if s.totalSize > maxSize {
			maxSize = s.totalSize
			bestSet = name
		}
	}

	nestedParts := sets[bestSet].parts
	logger.Info("Detected nested archive", "set", bestSet, "size", maxSize, "volumes", len(nestedParts))

	// Build VirtualFiles for each inner archive volume
	innerFiles := make(map[string][]filePart)
	for _, p := range nestedParts {
		innerFiles[p.name] = append(innerFiles[p.name], p)
	}

	var nestedFiles []UnpackableFile
	for name, fps := range innerFiles {
		sortByVolume(fps)

		compressed := false
		var vfParts []virtualPart
		var vOffset int64
		for _, p := range fps {
			if p.isCompressed {
				compressed = true
			}
			vfParts = append(vfParts, virtualPart{
				VirtualStart: vOffset,
				VirtualEnd:   vOffset + p.packedSize,
				VolFile:      p.volFile,
				VolOffset:    p.dataOffset,
			})
			vOffset += p.packedSize
		}

		if compressed {
			return nil, fmt.Errorf("nested archive %s is compressed", name)
		}

		totalSize := fps[0].unpackedSize
		if totalSize == 0 {
			totalSize = vOffset
		}
		nestedFiles = append(nestedFiles, NewVirtualFile(name, totalSize, vfParts))
	}

	logger.Info("Recursively scanning nested archive", "set", bestSet, "volumes", len(nestedFiles))
	return ScanArchive(nestedFiles)
}

// --- helpers ---

func filterRarFiles(files []UnpackableFile) []UnpackableFile {
	var result []UnpackableFile
	for _, f := range files {
		name := ExtractFilename(f.Name())
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ExtPar2) {
			continue
		}
		if strings.HasSuffix(lower, ExtRar) || strings.Contains(lower, ".part") || IsRarPart(lower) || IsSplitArchivePart(lower) {
			result = append(result, f)
		}
	}
	return result
}

func filterFirstVolumes(files []UnpackableFile) []UnpackableFile {
	var result []UnpackableFile
	for _, f := range files {
		name := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(name, ExtRar) && !strings.Contains(name, ".part") && !strings.Contains(name, ".r0") {
			result = append(result, f)
			continue
		}
		if IsMiddleRarVolume(name) {
			continue
		}
		result = append(result, f)
	}
	return result
}

func findFirstVolume(files []*loader.File) *loader.File {
	// Pass 1: definite first volumes
	for _, f := range files {
		lower := strings.ToLower(f.Name())
		if strings.HasSuffix(lower, ".par2") || strings.HasSuffix(lower, ".nzb") || strings.HasSuffix(lower, ".nfo") {
			continue
		}
		if (strings.HasSuffix(lower, ".rar") && !strings.Contains(lower, ".part")) ||
			strings.Contains(lower, ".part01.") || strings.Contains(lower, ".part1.") ||
			strings.HasSuffix(lower, ".r00") || strings.HasSuffix(lower, ".001") {
			return f
		}
	}
	// Pass 2: any .rar
	for _, f := range files {
		lower := strings.ToLower(f.Name())
		if strings.HasSuffix(lower, ".par2") || strings.HasSuffix(lower, ".nzb") || strings.HasSuffix(lower, ".nfo") {
			continue
		}
		if strings.HasSuffix(lower, ".rar") {
			return f
		}
	}
	return nil
}

func isMediaFile(info rardecode.ArchiveFileInfo) bool {
	name := info.Name
	isVideo := IsVideoFile(name) || strings.HasSuffix(strings.ToLower(name), ExtIso)
	isLarge := info.TotalUnpackedSize > 50*1024*1024
	lower := strings.ToLower(name)
	isArchive := strings.HasSuffix(lower, ExtRar) || strings.HasSuffix(lower, ExtZip) ||
		strings.HasSuffix(lower, Ext7z) || strings.HasSuffix(lower, ExtPar2) || IsRarPart(lower)
	return isVideo || (isLarge && !isArchive)
}

func archiveSetName(name string) string {
	lower := strings.ToLower(name)
	if idx := strings.LastIndex(lower, ".part"); idx != -1 {
		return name[:idx]
	}
	if idx := strings.LastIndex(lower, ".r"); idx != -1 && idx > len(lower)-5 {
		return name[:idx]
	}
	if strings.HasSuffix(lower, ExtRar) {
		return strings.TrimSuffix(strings.TrimSuffix(name, ExtRar), ".RAR")
	}
	return name
}

func sortByVolume(parts []filePart) {
	sort.Slice(parts, func(i, j int) bool {
		return volumeOrder(parts[i].volName) < volumeOrder(parts[j].volName)
	})
}

func volumeOrder(name string) string {
	return ExtractFilename(name)
}
