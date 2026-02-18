package search

import (
	"fmt"
	"sync"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
)

// TMDBResolver resolves movie/TV titles for text search.
type TMDBResolver interface {
	GetMovieTitle(imdbID, tmdbID string) (string, error)
	GetTVShowName(tmdbID, imdbID string) (string, error)
}

// RunIndexerSearches runs ID-based and text-based searches in parallel, merges and dedupes.
// Text search uses TMDB to resolve titles; when TMDB is unavailable, only ID search runs.
func RunIndexerSearches(idx indexer.Indexer, tmdbClient TMDBResolver, req indexer.SearchRequest, contentType string, contentIDs *session.AvailReportMeta, imdbForText, tmdbForText string) ([]*release.Release, error) {
	idReq := req
	idReq.Query = ""

	var textQuery string
	if tmdbClient != nil {
		if contentType == "movie" {
			if t, err := tmdbClient.GetMovieTitle(contentIDs.ImdbID, req.TMDBID); err == nil {
				textQuery = t
			}
		} else if req.Season != "" && req.Episode != "" {
			if name, err := tmdbClient.GetTVShowName(tmdbForText, imdbForText); err == nil {
				textQuery = fmt.Sprintf("%s S%sE%s", name, req.Season, req.Episode)
			}
		}
	}

	var idResp *indexer.SearchResponse
	var idErr error
	var textReleases []*release.Release
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		idResp, idErr = idx.Search(idReq)
	}()
	if textQuery != "" {
		wg.Add(1)
		textReq := indexer.SearchRequest{Query: textQuery, Cat: req.Cat, Limit: req.Limit, Season: req.Season, Episode: req.Episode}
		go func() {
			defer wg.Done()
			if resp, err := idx.Search(textReq); err == nil {
				indexer.NormalizeSearchResponse(resp)
				textReleases = FilterTextResultsByContent(resp.Releases, contentType, textQuery, req.Season, req.Episode)
			}
		}()
	}
	wg.Wait()

	if idErr != nil {
		return nil, fmt.Errorf("indexer search failed: %w", idErr)
	}
	indexer.NormalizeSearchResponse(idResp)
	idReleases := make([]*release.Release, 0, len(idResp.Releases)+len(textReleases))
	for _, rel := range idResp.Releases {
		if rel != nil {
			rel.QuerySource = "id"
			idReleases = append(idReleases, rel)
		}
	}
	for _, rel := range textReleases {
		if rel != nil {
			rel.QuerySource = "text"
			idReleases = append(idReleases, rel)
		}
	}
	if len(textReleases) > 0 {
		logger.Debug("Indexer dual search", "id", len(idResp.Releases), "text", len(textReleases))
	}
	return MergeAndDedupeSearchResults(idReleases), nil
}
