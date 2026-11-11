package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// breakdownRows caps how many host lines a 5xx alert lists. A burst is nearly
// always one or two apps; past that the "and N more" line says enough.
const breakdownRows = 5

// uriLimit keeps one scanner's long query string from swamping the message.
const uriLimit = 60

// Breakdown5xx names the hosts behind the 5xx responses in [from, to), one line
// each, ready to append to an alert message. A bare count ("5 5xx errors in 5
// min") gives no clue which app failed, so the alert used to mean a trip to the
// Caddy logs. total is the caller's own 5xx count for the window, used for the
// "and N more" line. Returns "" when the lookup fails, so the alert still goes
// out with just the count.
func Breakdown5xx(ctx context.Context, q *db.Queries, from, to time.Time, total int64) string {
	rows, err := q.Top5xxBetween(ctx, db.Top5xxBetweenParams{
		FromTs:   from,
		ToTs:     to,
		RowLimit: breakdownRows,
	})
	if err != nil {
		slog.Error("alert 5xx breakdown", "err", err)
		return ""
	}
	return format5xxBreakdown(rows, total)
}

func format5xxBreakdown(rows []db.Top5xxBetweenRow, total int64) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	var shown int64
	for _, r := range rows {
		shown += r.Cnt
		fmt.Fprintf(&b, "\n• %s: %d × %d ", r.Host, r.Cnt, r.Status)
		if r.Paths == 1 {
			fmt.Fprintf(&b, "on %s", truncate(r.SampleUri, uriLimit))
		} else {
			fmt.Fprintf(&b, "on %d paths, e.g. %s", r.Paths, truncate(r.SampleUri, uriLimit))
		}
	}
	// Rows arrive after the count, so a 5xx logged in between can make shown
	// exceed total. Only a genuine remainder gets a line.
	if rest := total - shown; rest > 0 {
		fmt.Fprintf(&b, "\n• and %d more", rest)
	}
	return b.String()
}
