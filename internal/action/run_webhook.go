package action

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/autobrr/autobrr/internal/domain"
	"github.com/autobrr/autobrr/internal/stringutils"
	"github.com/autobrr/autobrr/pkg/errors"
	"github.com/autobrr/autobrr/pkg/sharedhttp"
	"github.com/rs/zerolog"
)

// maxWebhookBody caps response reads. Sonarr/Radarr-shaped responses are
// typically <1 KB; the headroom fits structured payloads from custom
// endpoints without letting a misbehaving server exhaust memory.
const maxWebhookBody = 1024 * 1024

type webhookProcessResponse struct {
	Approved   *bool    `json:"approved"`
	Rejected   *bool    `json:"rejected"`
	Rejections []string `json:"rejections"`
}

func (s *Service) runWebhook(ctx context.Context, action *domain.Action) ([]string, error) {
	l := zerolog.Ctx(ctx)

	l.Debug().Msg("running Webhook action")

	l.Trace().Str("host", action.WebhookHost).Str("payload", stringutils.TruncateStr(action.WebhookData, 1024)).Msg("running Webhook action")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, action.WebhookHost, bytes.NewBufferString(action.WebhookData))
	if err != nil {
		return nil, errors.Wrap(err, "could not build request for webhook")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.userAgent)

	start := time.Now()
	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "could not make request for webhook")
	}

	defer sharedhttp.DrainAndClose(res)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("webhook returned non-2xx status: %d %s (url: %s)", res.StatusCode, http.StatusText(res.StatusCode), action.WebhookHost)
	}

	// A body larger than the cap is truncated, fails to unmarshal and is
	// treated as opaque success, same as any non-JSON body.
	body, err := io.ReadAll(io.LimitReader(res.Body, maxWebhookBody))
	if err != nil {
		return nil, errors.Wrap(err, "could not read webhook response body from %s", action.WebhookHost)
	}

	if len(body) > 0 {
		var parsed webhookProcessResponse
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr == nil {
			rejected := parsed.Rejected != nil && *parsed.Rejected
			explicitNotApproved := parsed.Approved != nil && !*parsed.Approved
			if rejected || explicitNotApproved {
				reasons := parsed.Rejections
				if len(reasons) == 0 {
					reasons = []string{"webhook rejected the release"}
				}
				l.Debug().Str("host", action.WebhookHost).Strs("rejections", reasons).Dur("duration", time.Since(start)).Msg("webhook rejected release")
				return reasons, nil
			}
		}
	}

	l.Info().Str("host", action.WebhookHost).Str("payload", stringutils.TruncateStr(action.WebhookData, 256)).Dur("duration", time.Since(start)).Msg("webhook action executed")

	return nil, nil
}
