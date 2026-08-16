// Copyright (c) 2021 - 2025, Ludvig Lundgren and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package action

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/autobrr/autobrr/internal/domain"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func newWebhookTestService() *Service {
	return &Service{
		log:        zerolog.Nop(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func Test_service_webhook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverStatus   int
		serverBody     string
		serverHandler  http.HandlerFunc // optional; overrides serverStatus/serverBody when set
		wantRejections []string
		wantErr        bool
	}{
		{
			name:           "200_empty_body_is_success",
			serverStatus:   http.StatusOK,
			serverBody:     "",
			wantRejections: nil,
			wantErr:        false,
		},
		{
			name:           "500_is_push_error",
			serverStatus:   http.StatusInternalServerError,
			serverBody:     "boom",
			wantRejections: nil,
			wantErr:        true,
		},
		{
			name:           "404_is_push_error",
			serverStatus:   http.StatusNotFound,
			serverBody:     "",
			wantRejections: nil,
			wantErr:        true,
		},
		{
			name:           "301_is_push_error",
			serverStatus:   http.StatusMovedPermanently,
			serverBody:     "",
			wantRejections: nil,
			wantErr:        true,
		},
		{
			name:           "200_non_json_body_is_success",
			serverStatus:   http.StatusOK,
			serverBody:     "<html><body>thanks</body></html>",
			wantRejections: nil,
			wantErr:        false,
		},
		{
			name:           "200_unrelated_json_is_success",
			serverStatus:   http.StatusOK,
			serverBody:     `{"id": 42, "ok": true}`,
			wantRejections: nil,
			wantErr:        false,
		},
		{
			name:           "200_approved_true_is_success",
			serverStatus:   http.StatusOK,
			serverBody:     `{"approved": true}`,
			wantRejections: nil,
			wantErr:        false,
		},
		{
			name:           "200_rejected_with_reasons",
			serverStatus:   http.StatusOK,
			serverBody:     `{"approved": false, "rejected": true, "rejections": ["Unknown Series", "Already grabbed"]}`,
			wantRejections: []string{"Unknown Series", "Already grabbed"},
			wantErr:        false,
		},
		{
			name:           "200_rejected_no_reasons_uses_default",
			serverStatus:   http.StatusOK,
			serverBody:     `{"rejected": true}`,
			wantRejections: []string{"webhook rejected the release"},
			wantErr:        false,
		},
		{
			name:           "200_approved_false_explicit_uses_default",
			serverStatus:   http.StatusOK,
			serverBody:     `{"approved": false}`,
			wantRejections: []string{"webhook rejected the release"},
			wantErr:        false,
		},
		{
			name:           "200_approved_and_rejected_both_true_treats_as_rejected",
			serverStatus:   http.StatusOK,
			serverBody:     `{"approved": true, "rejected": true}`,
			wantRejections: []string{"webhook rejected the release"},
			wantErr:        false,
		},
		{
			name: "200_oversized_body_truncates_to_success",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				// Build a body where:
				//   - Read in full: valid JSON, rejected:true (would return a rejection).
				//   - Read with the 1 MiB LimitReader cap: truncated mid-string,
				//     json.Unmarshal fails, falls through to success.
				// The closing `"}` lands past byte maxWebhookBody, so the cap drops it.
				prefix := []byte(`{"rejected":true,"pad":"`)
				suffix := []byte(`"}`)
				padLen := maxWebhookBody + 100 - len(prefix) - len(suffix)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(prefix)
				_, _ = w.Write(bytes.Repeat([]byte{'a'}, padLen))
				_, _ = w.Write(suffix)
			},
			wantRejections: nil,
			wantErr:        false,
		},
		{
			name: "server_closes_connection_is_push_error",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Errorf("ResponseWriter does not support hijacking")
					return
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Errorf("hijack failed: %v", err)
					return
				}
				_ = conn.Close()
			},
			wantRejections: nil,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := tt.serverHandler
			if handler == nil {
				handler = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tt.serverStatus)
					if tt.serverBody != "" {
						_, _ = w.Write([]byte(tt.serverBody))
					}
				}
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()

			s := newWebhookTestService()
			action := &domain.Action{
				Name:        tt.name,
				WebhookHost: srv.URL,
				WebhookData: `{"event":"push"}`,
			}

			rejections, err := s.webhook(context.Background(), action)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantRejections, rejections)
		})
	}
}

// fakeDownloadService mirrors DownloadService.DownloadRelease: it only reaches
// the indexer when the release does not already carry the torrent, so a test
// can count actual fetches rather than calls.
type fakeDownloadService struct {
	fetches int
}

func (f *fakeDownloadService) DownloadRelease(_ context.Context, rls *domain.Release) error {
	if len(rls.TorrentDataRawBytes) != 0 {
		return nil
	}

	f.fetches++
	rls.TorrentDataRawBytes = []byte("d8:announce0:4:infod4:name4:test6:lengthi1eee")
	rls.TorrentHash = "abc123"

	return nil
}

func (f *fakeDownloadService) ResolveMagnetURI(_ context.Context, _ *domain.Release) error {
	return nil
}

func newTestService(dl downloadService) *Service {
	return &Service{log: zerolog.Nop(), downloadSvc: dl}
}

// A release with several download client actions must only be fetched from the
// indexer once. The executors take the release by value, so the download has to
// land on the shared release here or every action refetches the same torrent.
func TestCheckActionPreconditions_DownloadsOncePerRelease(t *testing.T) {
	t.Parallel()

	dl := &fakeDownloadService{}
	svc := newTestService(dl)

	release := &domain.Release{TorrentName: "Test.Release-GRP", Protocol: domain.ReleaseProtocolTorrent}

	actions := []*domain.Action{
		{Name: "qbit", Type: domain.ActionTypeQbittorrent},
		{Name: "deluge", Type: domain.ActionTypeDelugeV2},
		{Name: "watch", Type: domain.ActionTypeWatchFolder},
	}

	for _, action := range actions {
		err := svc.CheckActionPreconditions(context.Background(), action, release)
		assert.NoError(t, err)
	}

	assert.Equal(t, 1, dl.fetches, "torrent should be fetched from the indexer once for all actions")
	assert.NotEmpty(t, release.TorrentDataRawBytes, "torrent should land on the shared release")
}

// Magnet releases carry no torrent to fetch, the client is handed the URI.
func TestCheckActionPreconditions_SkipsDownloadForMagnet(t *testing.T) {
	t.Parallel()

	dl := &fakeDownloadService{}
	svc := newTestService(dl)

	release := &domain.Release{
		TorrentName: "Test.Release-GRP",
		Protocol:    domain.ReleaseProtocolTorrent,
		MagnetURI:   "magnet:?xt=urn:btih:abc123",
	}

	err := svc.CheckActionPreconditions(context.Background(), &domain.Action{Type: domain.ActionTypeQbittorrent}, release)

	assert.NoError(t, err)
	assert.Equal(t, 0, dl.fetches, "magnet releases must not trigger a torrent download")
}

// Actions that never touch the torrent should not pull it down.
func TestCheckActionPreconditions_SkipsDownloadForArrActions(t *testing.T) {
	t.Parallel()

	dl := &fakeDownloadService{}
	svc := newTestService(dl)

	release := &domain.Release{TorrentName: "Test.Release-GRP", Protocol: domain.ReleaseProtocolTorrent}

	err := svc.CheckActionPreconditions(context.Background(), &domain.Action{Type: domain.ActionTypeRadarr}, release)

	assert.NoError(t, err)
	assert.Equal(t, 0, dl.fetches)
}

// A path macro needs a real file, and it has to exist before ParseMacros runs.
func TestCheckActionPreconditions_WritesTmpFileForPathMacro(t *testing.T) {
	t.Parallel()

	dl := &fakeDownloadService{}
	svc := newTestService(dl)

	release := &domain.Release{TorrentName: "Test.Release-GRP", Protocol: domain.ReleaseProtocolTorrent}
	action := &domain.Action{Type: domain.ActionTypeExec, ExecArgs: "--torrent {{.TorrentPathName}}"}

	err := svc.CheckActionPreconditions(context.Background(), action, release)
	assert.NoError(t, err)

	assert.Equal(t, 1, dl.fetches)
	assert.NotEmpty(t, release.TorrentTmpFile, "path macro requires a tmp file on disk")

	t.Cleanup(func() {
		_ = release.CleanupTemporaryFiles()
	})
}

// An exec action without torrent macros has nothing to download.
func TestCheckActionPreconditions_SkipsDownloadForPlainExec(t *testing.T) {
	t.Parallel()

	dl := &fakeDownloadService{}
	svc := newTestService(dl)

	release := &domain.Release{TorrentName: "Test.Release-GRP", Protocol: domain.ReleaseProtocolTorrent}
	action := &domain.Action{Type: domain.ActionTypeExec, ExecArgs: "--name {{.TorrentName}}"}

	err := svc.CheckActionPreconditions(context.Background(), action, release)

	assert.NoError(t, err)
	assert.Equal(t, 0, dl.fetches)
	assert.Empty(t, release.TorrentTmpFile)
}
