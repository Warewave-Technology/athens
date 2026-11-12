package store

import (
	"context"
	"log/slog"
	"time"
)

// RunJanitor purges expired sessions every 10 minutes and old audit records
// daily until ctx is done.
func RunJanitor(ctx context.Context, s Store, idleTTL time.Duration, retentionDays int, log *slog.Logger) {
	sessions := time.NewTicker(10 * time.Minute)
	audit := time.NewTicker(24 * time.Hour)
	defer sessions.Stop()
	defer audit.Stop()
	purgeAudit := func() {
		n, err := s.PurgeAudit(ctx, time.Now().AddDate(0, 0, -retentionDays))
		if err != nil && ctx.Err() == nil {
			log.Error("audit purge failed", "err", err)
		} else if n > 0 {
			log.Info("audit purged", "rows", n)
		}
	}
	purgeAudit()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sessions.C:
			if _, err := s.PurgeSessions(ctx, time.Now(), idleTTL); err != nil && ctx.Err() == nil {
				log.Error("session purge failed", "err", err)
			}
		case <-audit.C:
			purgeAudit()
		}
	}
}
