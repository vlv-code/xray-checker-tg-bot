// Package asn resolves node host IPs to their autonomous system (network
// operator) using a local db-ip asn-lite mmdb database. Every failure mode
// degrades to an empty string — ASN never blocks or breaks alerting.
package asn

import (
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"xray-checker/logger"
)

// DefaultDBURL points at db-ip's free asn-lite database (CC-BY 4.0). The URL
// embeds a month; override with ASN_DB_URL after db-ip publishes a newer one.
const DefaultDBURL = "https://download.db-ip.com/free/dbip-asn-lite-2026-08.mmdb.gz"

// downloadTimeout bounds the one-shot DB download at startup.
const downloadTimeout = 90 * time.Second

// EnsureDB downloads the gzipped mmdb from url to path unless a file already
// exists. The download is atomic (tmp + rename) so an interrupted fetch
// never leaves a truncated database behind.
func EnsureDB(path, url string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("asn db is not valid gzip: %w", err)
	}
	defer gz.Close()

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, gz); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing asn db: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("closing asn db: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("moving asn db into place: %w", err)
	}
	logger.Info("ASN database downloaded to %s", path)
	return nil
}

// DB wraps the mmdb reader. Safe for concurrent use.
type DB struct {
	reader *maxminddb.Reader
}

type asnRecord struct {
	AutonomousSystemNumber       uint   `maxminddb:"autonomous_system_number"`
	AutonomousSystemOrganization string `maxminddb:"autonomous_system_organization"`
}

// Open memory-maps the mmdb file at path.
func Open(path string) (*DB, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening asn db %s: %w", path, err)
	}
	return &DB{reader: reader}, nil
}

// Close releases the underlying reader.
func (d *DB) Close() error {
	if d == nil || d.reader == nil {
		return nil
	}
	return d.reader.Close()
}

// NopLookup is the LookupFunc used when no database is available.
func NopLookup(string) string { return "" }

// Lookup returns "AS<number> <organization>" for a public IP, "" otherwise.
func (d *DB) Lookup(ipStr string) string {
	if d == nil || d.reader == nil {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ""
	}
	var rec asnRecord
	if err := d.reader.Lookup(ip, &rec); err != nil {
		return ""
	}
	return formatASN(rec.AutonomousSystemNumber, rec.AutonomousSystemOrganization)
}

// formatASN renders the display form; zero ASN renders empty.
func formatASN(num uint, org string) string {
	if num == 0 {
		return ""
	}
	if org == "" {
		return fmt.Sprintf("AS%d", num)
	}
	return fmt.Sprintf("AS%d %s", num, org)
}
