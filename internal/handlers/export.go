package handlers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	db "github.com/exploded/monitor/db/sqlc"
)

// ExportSearch streams search results as CSV or JSON download.
func (h *Handler) ExportSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := h.parseSearch(r)
	format := r.URL.Query().Get("format")

	params := db.SearchRequestsExportParams{
		HostFilter:   f.host,
		IpFilter:     f.ip,
		StatusFilter: f.status,
		UaFilter:     f.ua,
		FromTs:       f.from,
		ToTs:         f.to,
	}
	var results []db.SearchRequestsExportRow
	var err error
	if f.slowest {
		var rows []db.SearchRequestsExportSlowestRow
		rows, err = h.q.SearchRequestsExportSlowest(ctx, db.SearchRequestsExportSlowestParams{
			HostFilter: f.host, IpFilter: f.ip, StatusFilter: f.status, UaFilter: f.ua,
			FromTs: f.from, ToTs: f.to, LatencyExclude: h.cfg.LatencyExcludeJSON(),
		})
		for _, row := range rows {
			results = append(results, db.SearchRequestsExportRow(row))
		}
	} else {
		results, err = h.q.SearchRequestsExport(ctx, params)
	}
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
				r.Ts.In(melbourne).Format("2006-01-02 15:04:05"),
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
