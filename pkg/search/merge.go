package search

import (
	"sort"
	"strconv"
	"strings"

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

// FilterTextResultsByContent keeps only releases where ptt-parsed title matches the content.
func FilterTextResultsByContent(releases []*release.Release, contentType, textQuery, season, episode string) []*release.Release {
	if contentType != "movie" && contentType != "series" {
		return releases
	}
	expectTitle := release.NormalizeTitle(textQuery)
	expectSeason, _ := strconv.Atoi(season)
	expectEpisode, _ := strconv.Atoi(episode)
	var expectShow string
	if contentType == "series" && (expectSeason > 0 || expectEpisode > 0) {
		expectShow = release.NormalizeTitle(strings.Split(textQuery, " S")[0])
	}

	var out []*release.Release
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		parsed := parser.ParseReleaseTitle(rel.Title)
		if contentType == "movie" {
			got := release.NormalizeTitle(parsed.Title)
			if got == "" {
				continue
			}
			if !strings.Contains(expectTitle, got) && !strings.Contains(got, expectTitle) {
				expectBase := expectTitle
				for i := len(expectBase) - 1; i >= 0; i-- {
					if expectBase[i] >= '0' && expectBase[i] <= '9' {
						continue
					}
					if i < len(expectBase)-1 && expectBase[i] == ' ' {
						expectBase = strings.TrimSpace(expectBase[:i])
					}
					break
				}
				if !strings.Contains(got, expectBase) && !strings.Contains(expectBase, got) {
					continue
				}
			}
		} else {
			gotShow := release.NormalizeTitle(parsed.Title)
			if gotShow == "" {
				continue
			}
			if expectShow != "" && !strings.Contains(gotShow, expectShow) && !strings.Contains(expectShow, gotShow) {
				continue
			}
			if expectSeason > 0 && parsed.Season != expectSeason {
				continue
			}
			if expectEpisode > 0 && parsed.Episode != expectEpisode {
				continue
			}
		}
		out = append(out, rel)
	}
	return out
}

// MergeAndDedupeSearchResults merges ID and text results, preferring ID-based when duplicates.
func MergeAndDedupeSearchResults(releases []*release.Release) []*release.Release {
	sort.SliceStable(releases, func(i, j int) bool {
		return releases[i].QuerySource == "id" && releases[j].QuerySource != "id"
	})
	seenTitle := make(map[string]bool)
	var result []*release.Release
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		normTitle := release.NormalizeTitle(rel.Title)
		if normTitle == "" {
			continue
		}
		if seenTitle[normTitle] {
			continue
		}
		seenTitle[normTitle] = true
		result = append(result, rel)
	}
	return result
}
