package handlers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	db "github.com/exploded/monitor/db/sqlc"
)

// ExportSearch streams search results as CSV or JSON download.
func (h *Handler) ExportSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	host := q.Get("host")
	ip := q.Get("ip")
	ua := q.Get("ua")
	statusStr := q.Get("status")
	fromStr := q.Get("from")
	toStr := q.Get("to")
	format := q.Get("format")

	var statusFilter int64
	if statusStr != "" {
		statusFilter, _ = strconv.ParseInt(statusStr, 10, 64)
	}

	from := time.Now().UTC().Add(-24 * time.Hour)
	if fromStr != "" {
		if t, err := time.Parse("2006-01-02", fromStr); err == nil {
			from = t
		}
	}
	to := time.Now().UTC()
	if toStr != "" {
		if t, err := time.Parse("2006-01-02", toStr); err == nil {
			to = t.Add(24*time.Hour - time.Second)
		}
	}

	results, err := h.q.SearchRequestsExport(ctx, db.SearchRequestsExportParams{
		HostFilter:   host,
		IpFilter:     ip,
		StatusFilter: statusFilter,
		UaFilter:     ua,
		FromTs:       from,
		ToTs:         to,
	})
	if err != nil {
		http.Error(w, "query error", http.StatusInternalServerError)
		return
	}

	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="requests.json"`)
		json.NewEncoder(w).Encode(results)
	default:
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="requests.csv"`)
		cw := csv.NewWriter(w)
		cw.Write([]string{"ID", "Timestamp", "Host", "IP", "Method", "URI", "Status", "Size", "UserAgent", "DurationMs", "IsBot"})
		for _, r := range results {
			cw.Write([]string{
				strconv.FormatInt(r.ID, 10),
				r.Ts.Format("2006-01-02 15:04:05"),
				r.Host,
				r.ClientIp,
				r.Method,
				r.Uri,
				strconv.FormatInt(r.Status, 10),
				strconv.FormatInt(r.Size, 10),
				r.UserAgent,
				fmt.Sprintf("%.1f", r.DurationMs),
				strconv.FormatInt(r.IsBot, 10),
			})
		}
		cw.Flush()
	}
}
