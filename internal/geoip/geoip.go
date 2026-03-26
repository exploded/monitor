package geoip

import (
	"log/slog"
	"net"

	"github.com/oschwald/maxminddb-golang"
)

// geoRecord matches the subset of the MaxMind GeoLite2-City database we use.
type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// Resolver wraps a MaxMind .mmdb reader for IP geolocation.
type Resolver struct {
	db *maxminddb.Reader
}

// New opens the .mmdb file at path. Returns (nil, nil) if path is empty,
// so callers can treat a nil Resolver as a no-op.
func New(path string) (*Resolver, error) {
	if path == "" {
		return nil, nil
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		slog.Warn("geoip database not available", "path", path, "err", err)
		return nil, nil // graceful degradation
	}
	slog.Info("geoip database loaded", "path", path)
	return &Resolver{db: db}, nil
}

// Lookup resolves an IP to country code and city name.
// Safe to call on a nil receiver.
func (r *Resolver) Lookup(ipStr string) (country, city string) {
	if r == nil || r.db == nil {
		return "", ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", ""
	}
	var rec geoRecord
	if err := r.db.Lookup(ip, &rec); err != nil {
		return "", ""
	}
	return rec.Country.ISOCode, rec.City.Names["en"]
}

// Close closes the underlying database.
func (r *Resolver) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
