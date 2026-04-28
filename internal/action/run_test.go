// Copyright (c) 2021 - 2025, Ludvig Lundgren and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package action

import (
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
