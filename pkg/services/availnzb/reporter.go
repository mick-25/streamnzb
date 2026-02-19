package availnzb

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
	"sync"
)

// ProviderHostsSource provides provider hostnames for reporting.
type ProviderHostsSource interface {
	GetProviderHosts() []string
}

// Reporter reports availability to AvailNZB.
type Reporter struct {
	client      *Client
	providerSrc ProviderHostsSource
	reported    sync.Map // sessionID -> struct{} for dedup; ReportGood is called on every play request (seeking, etc.)
}

// NewReporter creates a reporter.
func NewReporter(client *Client, providerSrc ProviderHostsSource) *Reporter {
	return &Reporter{client: client, providerSrc: providerSrc}
}

// ReportGood reports successful fetch/stream to AvailNZB. Deduplicated per session (avoids spam from each range/seek request).
func (r *Reporter) ReportGood(sess *session.Session) {
	if _, loaded := r.reported.LoadOrStore(sess.ID, struct{}{}); loaded {
		return // already reported for this session
	}
	r.report(sess, true)
}

// ReportBad reports bad/unstreamable release to AvailNZB as unavailable.
func (r *Reporter) ReportBad(sess *session.Session, reason string) {
	if reason != "" {
		logger.Info("Reporting bad/unstreamable release to AvailNZB", "session", sess.ID, "reason", reason)
	}
	r.report(sess, false)
}

// ReportRAR reports RAR releases to AvailNZB as available with compression_type=rar.
func (r *Reporter) ReportRAR(sess *session.Session) {
	logger.Info("Reporting RAR release to AvailNZB (compression_type)", "session", sess.ID)
	r.report(sess, true)
}

func (r *Reporter) report(sess *session.Session, available bool) {
	if r.client == nil || r.client.BaseURL == "" {
		return
	}
	go func() {
		releaseURL := sess.ReleaseURL()
		if releaseURL == "" {
			return
		}
		if release.IsPrivateReleaseURL(releaseURL) {
			logger.Debug("Skipping AvailNZB report: release URL is private", "url", releaseURL)
			return
		}
		meta := ReportMeta{ReleaseName: sess.ReportReleaseName(), Size: sess.ReportSize()}
		if ids := sess.ContentIDs; ids != nil {
			if ids.ImdbID != "" {
				meta.ImdbID = ids.ImdbID
			} else if ids.TvdbID != "" {
				meta.TvdbID = ids.TvdbID
				meta.Season = ids.Season
				meta.Episode = ids.Episode
			}
		}
		if meta.ImdbID == "" && meta.TvdbID == "" {
			return
		}
		if meta.ReleaseName == "" {
			return
		}
		if sess.NZB != nil {
			meta.CompressionType = sess.NZB.CompressionType()
		}
		providerURL := "ALL"
		if hosts := r.providerSrc.GetProviderHosts(); len(hosts) > 0 {
			providerURL = hosts[0]
		}
		_ = r.client.ReportAvailability(releaseURL, providerURL, available, meta)
	}()
}
