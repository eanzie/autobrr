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
	"github.com/autobrr/autobrr/internal/logger"

	"github.com/stretchr/testify/assert"
)

func newWebhookTestService() *service {
	return &service{
		log:        logger.Mock().With().Logger(),
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
			release := domain.Release{TorrentName: "Test.Release.2026"}

			rejections, err := s.webhook(context.Background(), action, release)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantRejections, rejections)
		})
	}
}
