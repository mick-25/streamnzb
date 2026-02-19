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

// StreamFromBlueprint returns a reader over the virtual file. We do not use rardecode
// for the read path: rardecode is only used for scanning (ListArchiveInfo) to get
// DataOffset per volume. For STORE mode, the bytes at DataOffset in each volume are
// the raw file content, so we read via the loader at VolOffset (DataOffset) and never
// decode through rardecode.Reader (which is sequential and would force "download from
// start" on seek).
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

	// Fail fast: if any scanned volume already exceeded its failure threshold,
	// the release is dead and there's no point building a blueprint.
	for _, f := range firstVols {
		if fc, ok := f.(interface{ IsFailed() bool }); ok && fc.IsFailed() {
			logger.Error("First volume failed too many segments, aborting scan", "file", f.Name())
			return nil, fmt.Errorf("first volume unavailable: %w", loader.ErrTooManyZeroFills)
		}
	}

	// No media found: likely a nested archive (RAR-in-RAR). Scan remaining
	// outer volumes to discover inner files that start in later volumes.
	// Only triggers when the first volume was actually parseable (len(parts) > 0)
	// but contained no video. If parts is empty, the first volume's data is too
	// corrupt for rardecode to read headers, so the full scan would also fail.
	hasMedia := false
	for _, p := range parts {
		if p.isMedia {
			hasMedia = true
			break
		}
	}
	if !hasMedia && len(parts) > 0 && len(rarFiles) > len(firstVols) {
		logger.Debug("No media in first volumes, running full multi-volume scan for nested archive")
		fullParts := scanFullArchive(rarFiles)
		if len(fullParts) > 0 {
			parts = fullParts
		}
	}

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

// excludeFiles returns files from all that are not in exclude (by Name()).
func excludeFiles(all, exclude []UnpackableFile) []UnpackableFile {
	skip := make(map[string]bool, len(exclude))
	for _, f := range exclude {
		skip[f.Name()] = true
	}
	var result []UnpackableFile
	for _, f := range all {
		if !skip[f.Name()] {
			result = append(result, f)
		}
	}
	return result
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
				rardecode.SkipVolumeCheck,
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

// scanFullArchive scans all volumes as a single multi-volume archive set.
// rardecode traverses volumes in order and seeks over data blocks, so only
// headers are downloaded. This gives complete FilePartInfo for every inner
// file, including data that spans across multiple outer volumes.
func scanFullArchive(rarFiles []UnpackableFile) []filePart {
	sort.Slice(rarFiles, func(i, j int) bool {
		return volumeOrder(rarFiles[i].Name()) < volumeOrder(rarFiles[j].Name())
	})

	fileMap := make(map[string]UnpackableFile, len(rarFiles))
	var firstName string
	for _, f := range rarFiles {
		clean := ExtractFilename(f.Name())
		fileMap[clean] = f
		if firstName == "" && !IsMiddleRarVolume(strings.ToLower(clean)) {
			firstName = clean
		}
	}
	if firstName == "" {
		firstName = ExtractFilename(rarFiles[0].Name())
	}

	// Ensure segment maps so seeking is possible during the scan
	for _, f := range rarFiles {
		if sm, ok := f.(segmentMapper); ok {
			sm.EnsureSegmentMap()
		}
	}

	fsys := NewNZBFSFromMap(fileMap)
	logger.Debug("Full archive scan starting", "first", firstName, "volumes", len(rarFiles))

	infos, err := rardecode.ListArchiveInfo(firstName,
		rardecode.FileSystem(fsys),
		rardecode.ParallelRead(true),
	)
	if err != nil {
		logger.Debug("Full archive scan failed", "err", err)
		return nil
	}

	var result []filePart
	for _, info := range infos {
		if info.Name == "" {
			continue
		}
		logger.Debug("Full scan found file", "name", info.Name, "size", info.TotalUnpackedSize, "parts", len(info.Parts))

		compressed := false
		for _, p := range info.Parts {
			if p.CompressionMethod != "stored" {
				compressed = true
			}
		}

		for _, p := range info.Parts {
			volFile := fileMap[ExtractFilename(p.Path)]
			if volFile == nil {
				logger.Debug("Volume not found in map", "path", p.Path)
				continue
			}
			result = append(result, filePart{
				name:         info.Name,
				unpackedSize: info.TotalUnpackedSize,
				dataOffset:   p.DataOffset,
				packedSize:   p.PackedSize,
				volFile:      volFile,
				volName:      volFile.Name(),
				isMedia:      isMediaFile(info),
				isCompressed: compressed,
			})
		}
	}
	logger.Debug("Full archive scan complete", "files", len(infos), "parts", len(result))
	return result
}

// --- blueprint construction ---

func buildBlueprint(parts []filePart, allRarFiles []UnpackableFile) (*ArchiveBlueprint, error) {
	bestName := selectMainFile(parts)

	// When direct media is dwarfed by archive content, the media is likely
	// just a sample and the real movie lives inside a nested archive.
	if bestName != "" {
		var mediaTotal, archiveTotal int64
		for _, p := range parts {
			if p.isMedia {
				mediaTotal += p.packedSize
			} else if IsArchiveFile(p.name) {
				archiveTotal += p.packedSize
			}
		}
		if archiveTotal > mediaTotal*2 {
			logger.Info("Archive content outweighs direct media, trying nested archive first",
				"media", mediaTotal, "archive", archiveTotal, "sample", bestName)
			if bp, err := tryNestedArchive(parts); err == nil {
				return bp, nil
			}
		}
	}

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
	for i, p := range mainParts {
		bp.Parts = append(bp.Parts, VirtualPartDef{
			VirtualStart: vOffset,
			VirtualEnd:   vOffset + p.packedSize,
			VolFile:      p.volFile,
			VolOffset:    p.dataOffset,
		})
		if i < 3 || i >= len(mainParts)-2 {
			logger.Debug("Blueprint part", "idx", i, "vStart", vOffset, "vEnd", vOffset+p.packedSize, "volOff", p.dataOffset, "packed", p.packedSize)
		}
		vOffset += p.packedSize
	}

	logger.Debug("Blueprint total", "vOffset", vOffset, "headerSize", headerSize, "parts", len(mainParts))

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
	// overhead AND the exact packed data size per continuation volume.
	// Using f.Size() - headerOverhead is wrong because f.Size() is based on
	// estimated segment sizes (the last segment's decoded size is a ratio
	// estimate). Even ~80 bytes of error per volume causes cumulative
	// misalignment of VirtualStart/VirtualEnd, so seeks to later volumes
	// read from the wrong byte offset and produce corrupt data.
	probe := probeContinuation(allRarFiles, startIdx, name)
	if probe.dataOffset > 0 {
		logger.Debug("Probed continuation volume", "dataOffset", probe.dataOffset, "packedSize", probe.packedSize)
	}

	first := mainParts[0]
	result := []filePart{first}

	numContVolumes := len(allRarFiles) - startIdx - 1
	if numContVolumes <= 0 {
		return result
	}

	// For standard RAR splits, all non-last continuation volumes have the
	// same packed data size. Use the probed PackedSize for them and derive
	// the last volume's size from the total file size.
	contPackedSize := probe.packedSize
	contDataOffset := probe.dataOffset

	// Calculate the last volume's exact data size from the total.
	// totalFileData = firstPart + (numContVolumes-1)*contPackedSize + lastPartData
	var lastPartData int64
	if contPackedSize > 0 && numContVolumes > 1 {
		lastPartData = headerSize - first.packedSize - int64(numContVolumes-1)*contPackedSize
	} else if contPackedSize > 0 && numContVolumes == 1 {
		lastPartData = headerSize - first.packedSize
	}

	added := 0
	for i := startIdx + 1; i < len(allRarFiles); i++ {
		f := allRarFiles[i]
		if sm, ok := f.(segmentMapper); ok {
			sm.EnsureSegmentMap()
		}
		if f.Size() <= 0 {
			continue
		}

		isLastVolume := i == len(allRarFiles)-1
		var dataSize int64
		if contPackedSize > 0 {
			if isLastVolume && lastPartData > 0 {
				dataSize = lastPartData
			} else if !isLastVolume {
				dataSize = contPackedSize
			} else {
				// Last volume, but couldn't compute lastPartData (only 1 cont volume)
				dataSize = contPackedSize
			}
		} else {
			// Fallback: no probe data, use estimated size
			dataSize = f.Size() - contDataOffset
		}

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
		added++
	}
	logger.Debug("Manual volume aggregation", "added", added, "total", len(result))
	return result
}

// continuationProbe holds exact offset and packed size for continuation volumes.
type continuationProbe struct {
	dataOffset int64
	packedSize int64
}

func probeContinuation(allRarFiles []UnpackableFile, startIdx int, targetName string) continuationProbe {
	if startIdx+1 >= len(allRarFiles) {
		return continuationProbe{}
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
		return continuationProbe{}
	}

	lowerTarget := strings.ToLower(targetName)
	for _, info := range infos {
		if strings.ToLower(info.Name) != lowerTarget {
			continue
		}
		if len(info.Parts) >= 2 {
			return continuationProbe{
				dataOffset: info.Parts[1].DataOffset,
				packedSize: info.Parts[1].PackedSize,
			}
		}
	}
	return continuationProbe{}
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
	for _, p := range nestedParts {
		logger.Debug("Nested archive part", "name", p.name, "volName", p.volName, "packed", p.packedSize, "unpacked", p.unpackedSize)
	}

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

	for _, nf := range nestedFiles {
		logger.Debug("Nested VirtualFile", "name", nf.Name(), "size", nf.Size(), "extracted", ExtractFilename(nf.Name()))
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
			logger.Debug("filterRarFiles: skip par2", "name", name)
			continue
		}
		if strings.HasSuffix(lower, ExtRar) || strings.Contains(lower, ".part") || IsRarPart(lower) || IsSplitArchivePart(lower) {
			result = append(result, f)
		} else {
			logger.Debug("filterRarFiles: skip non-rar", "name", name)
		}
	}
	return result
}

func filterFirstVolumes(files []UnpackableFile) []UnpackableFile {
	var result []UnpackableFile
	for _, f := range files {
		name := strings.ToLower(ExtractFilename(f.Name()))
		if strings.HasSuffix(name, ExtRar) && !strings.Contains(name, ".part") && !strings.Contains(name, ".r0") {
			logger.Debug("filterFirstVolumes: accept .rar first vol", "name", name)
			result = append(result, f)
			continue
		}
		if IsMiddleRarVolume(name) {
			logger.Debug("filterFirstVolumes: skip middle vol", "name", name)
			continue
		}
		logger.Debug("filterFirstVolumes: accept fallthrough", "name", name)
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
	clean := strings.ToLower(ExtractFilename(name))
	// Old-style naming: .rar is first (vol 0), .r00 = vol 1, .r01 = vol 2, etc.
	// Alphabetically .rar sorts after .r00-.r99, so we remap to a sortable key.
	if strings.HasSuffix(clean, ".rar") && !strings.Contains(clean, ".part") {
		return clean[:len(clean)-4] + ".r!!" // '!' < '0', so sorts before .r00
	}
	return clean
}
