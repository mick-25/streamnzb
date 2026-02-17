package unpack

import (
	"errors"
	"fmt"
	"sort"
	"streamnzb/pkg/loader"
	"streamnzb/pkg/nzb"
	"strings"
)

// Identify7zParts filters and sorts 7z files, handling split archives correctly
func Identify7zParts(files []*loader.File) ([]*loader.File, error) {
	var candidates []*loader.File

	// 1. Initial Filter: extensions
	for _, f := range files {
		// Use ExtractFilename to get the actual filename from subject
		name := strings.ToLower(ExtractFilename(f.Name()))

		// Must have .7z
		if !strings.Contains(name, ".7z") {
			continue
		}
		// Explicitly exclude .par2
		if strings.HasSuffix(name, ".par2") {
			continue
		}
		// Explicitly exclude .nzb / .nfo if they somehow got here (unlikely but safe)
		if strings.HasSuffix(name, ".nzb") || strings.HasSuffix(name, ".nfo") {
			continue
		}

		candidates = append(candidates, f)
	}

	if len(candidates) == 0 {
		return nil, errors.New("no 7z files found")
	}

	// 2. Sort by name (using logical filenames)
	sort.Slice(candidates, func(i, j int) bool {
		nameI := strings.ToLower(ExtractFilename(candidates[i].Name()))
		nameJ := strings.ToLower(ExtractFilename(candidates[j].Name()))
		return nameI < nameJ
	})

	// 3. Grouping Strategy
	// We might have multiple sets (e.g. sample.7z and movie.7z.001).
	// Or just junk files.
	// Simple heuristic: Find the "main" set.
	// If we find a .001 file, that defines a set.
	// If we find a standalone .7z, that defines a set.

	// Map baseName -> list of files
	sets := make(map[string][]*loader.File)

	for _, f := range candidates {
		name := ExtractFilename(f.Name())
		lower := strings.ToLower(name)

		var key string
		if strings.Contains(lower, ".7z.") {
			// Split archive: Movie.7z.001 -> Key: Movie.7z
			// Find the index of .7z
			idx := strings.Index(lower, ".7z")
			if idx != -1 {
				key = lower[:idx+3] // Include .7z
			} else {
				key = lower // Falback
			}
		} else if strings.HasSuffix(lower, ".7z") {
			// Standalone: Movie.7z
			key = lower
		} else {
			// Weird case? Movie.7z.part1?
			key = lower
		}

		sets[key] = append(sets[key], f)
	}

	// Pick the best set
	var bestSet []*loader.File
	var bestSetScore int64 // Total size? Or count?

	for _, set := range sets {
		var size int64
		hasOne := false
		for _, f := range set {
			size += f.Size()
			lower := strings.ToLower(ExtractFilename(f.Name()))
			if strings.HasSuffix(lower, ".7z.001") || strings.HasSuffix(lower, ".7z") {
				hasOne = true
			}
		}

		// Bonus for having the first part or being a standalone 7z
		if !hasOne && len(set) > 0 {
			// If we only have .002, .003 but not .001, strictly speaking we are broken.
			// But maybe we should still try?
			// For now, let's just compare sizes.
		}

		// Update best set if this one is larger, or if we haven't picked one yet
		// (handling 0-byte files gracefully, though unlikely in production)
		if bestSet == nil || size > bestSetScore {
			bestSetScore = size
			bestSet = set
		} else if size == bestSetScore {
			// Tie-breaker: prefer set with .001/primary file
			if hasOne {
				bestSet = set
			}
		}
	}

	if len(bestSet) == 0 {
		return nil, errors.New("no valid 7z sets found")
	}

	// Final sort of the best set (using logical filenames)
	sort.Slice(bestSet, func(i, j int) bool {
		nameI := strings.ToLower(ExtractFilename(bestSet[i].Name()))
		nameJ := strings.ToLower(ExtractFilename(bestSet[j].Name()))
		return nameI < nameJ
	})

	return bestSet, nil
}

// Validate7zArchive checks if the NZB contains a valid/complete 7z archive.
// Returns nil if valid or if no 7z files are present (ignored).
// Returns error if 7z files are present but incomplete/broken.
func Validate7zArchive(files []nzb.File) error {
	var candidates []*nzb.File
	// 1. Initial Filter
	for i := range files {
		f := &files[i]
		// Use ExtractFilename to get the actual filename from subject
		name := strings.ToLower(ExtractFilename(f.Subject))

		// Must have .7z
		if !strings.Contains(name, ".7z") {
			continue
		}
		// Explicitly exclude .par2, .nzb, .nfo
		if strings.HasSuffix(name, ".par2") || strings.HasSuffix(name, ".nzb") || strings.HasSuffix(name, ".nfo") {
			continue
		}

		candidates = append(candidates, f)
	}

	if len(candidates) == 0 {
		return nil // No 7z files, nothing to validate
	}

	// 2. Grouping (similar to Identify7zParts but for validation)
	sets := make(map[string][]*nzb.File)
	for _, f := range candidates {
		name := ExtractFilename(f.Subject)
		lower := strings.ToLower(name)
		var key string
		if strings.Contains(lower, ".7z.") {
			// Split archive: Movie.7z.001 -> Key: Movie.7z
			idx := strings.Index(lower, ".7z")
			if idx != -1 {
				key = lower[:idx+3] // Include .7z
			} else {
				key = lower
			}
		} else if strings.HasSuffix(lower, ".7z") {
			key = lower
		} else {
			key = lower
		}
		sets[key] = append(sets[key], f)
	}

	// 3. Pick Best Set
	var bestSet []*nzb.File
	var bestSetScore int64

	for _, set := range sets {
		var size int64
		hasOne := false
		for _, f := range set {
			// Calculate file size from segments
			fileSize := int64(0)
			for _, s := range f.Segments {
				fileSize += s.Bytes
			}
			size += fileSize

			lower := strings.ToLower(ExtractFilename(f.Subject))
			if strings.HasSuffix(lower, ".7z.001") || strings.HasSuffix(lower, ".7z") {
				hasOne = true
			}
		}

		// Selection logic
		if bestSet == nil || size > bestSetScore {
			bestSetScore = size
			bestSet = set
		} else if size == bestSetScore {
			if hasOne {
				bestSet = set
			}
		}
	}

	if len(bestSet) == 0 {
		return nil
	}

	// 4. Verification
	// Sort by name
	sort.Slice(bestSet, func(i, j int) bool {
		nameI := strings.ToLower(ExtractFilename(bestSet[i].Subject))
		nameJ := strings.ToLower(ExtractFilename(bestSet[j].Subject))
		return nameI < nameJ
	})

	// Check if split archive
	firstRawSubject := bestSet[0].Subject
	first := strings.ToLower(ExtractFilename(firstRawSubject))
	// Identify if it's supposed to be split
	isSplit := false
	for _, f := range bestSet {
		if strings.Contains(strings.ToLower(ExtractFilename(f.Subject)), ".7z.") {
			isSplit = true
			break
		}
	}

	if !isSplit {
		// Standalone .7z files.
		// If we only have one file "Movie.7z" it's valid.
		return nil
	}

	// If split, check sequential parts
	// First part MUST be .001 (or .7z.001)
	if !strings.HasSuffix(first, ".001") {
		return fmt.Errorf("split 7z archive missing part .001 (first found: %s)", first)
	}

	// Check sequence of all files in the set
	// They are sorted by name, so should be .001, .002, .003
	for i, f := range bestSet {
		expectedSuffix := fmt.Sprintf(".%03d", i+1)
		name := strings.ToLower(ExtractFilename(f.Subject))
		if !strings.HasSuffix(name, expectedSuffix) {
			return fmt.Errorf("7z archive sequence error: expected part %s, found %s", expectedSuffix, name)
		}
	}

	// Double check: Try to parse "Total Parts" from subject if possible
	// Common formats: (1/20), [1/20], "1 of 20"
	// We only strictly enforce this if we find a clear pattern.
	totalParts := parseTotalParts(firstRawSubject)
	if totalParts > 0 && len(bestSet) != totalParts {
		return fmt.Errorf("7z archive missing parts: found %d, expected %d", len(bestSet), totalParts)
	}

	return nil
}

// parseTotalParts attempts to extract the total number of parts from a subject string
func parseTotalParts(subject string) int {
	// (1/20) or [01/20]
	// Regex might be overkill, let's look for "1/XX)" or "1/XX]"

	// Normalize
	s := strings.ToLower(subject)

	// Look for pattern "1/(\d+)"
	// Simple scan: find "1/" then parse digits until non-digit
	idx := strings.Index(s, "1/")
	if idx == -1 {
		// Try "01/"
		idx = strings.Index(s, "01/")
	}
	if idx == -1 {
		// Try "001/"
		idx = strings.Index(s, "001/")
	}

	if idx != -1 {
		// Found start. Now find digits after slash.
		slashIdx := strings.Index(s[idx:], "/") + idx
		rest := s[slashIdx+1:]

		end := 0
		for end < len(rest) && isDigit(rest[end]) {
			end++
		}

		if end > 0 {
			// Found some digits
			var total int
			if _, err := fmt.Sscanf(rest[:end], "%d", &total); err == nil {
				return total
			}
		}
	}

	return 0
}
