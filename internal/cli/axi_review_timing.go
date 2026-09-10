package cli

import (
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	toon "github.com/toon-format/toon-go"
)

func reviewTimingField(t *db.ReviewTiming) toon.Field {
	if t == nil {
		return toon.Field{Key: "review_timing", Value: "unknown"}
	}
	startedAt := time.Unix(t.StartedAt, 0)
	if t.StartedAtMS != nil {
		startedAt = time.UnixMilli(*t.StartedAtMS)
	}
	return toon.Field{Key: "review_timing", Value: toon.NewObject(
		toon.Field{Key: "started_at", Value: startedAt.UTC().Format(time.RFC3339Nano)},
		toon.Field{Key: "status", Value: string(t.Status)},
		toon.Field{Key: "total_ms", Value: t.TotalMS},
		toon.Field{Key: "complete", Value: t.Complete},
		toon.Field{Key: "round_count", Value: t.RoundCount},
		toon.Field{Key: "review_ms", Value: t.ReviewMS},
		toon.Field{Key: "fix_ms", Value: t.FixMS},
		toon.Field{Key: "turns", Value: t.Turns},
	)}
}
