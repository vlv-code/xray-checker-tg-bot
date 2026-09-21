package xray

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"xray-checker/config"
	"xray-checker/logger"
)

const (
	geoSiteURL  = "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat"
	geoIPURL    = "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat"
	geoSiteFile = "geo/geosite.dat"
	geoIPFile   = "geo/geoip.dat"

	// minDatFileSize: geo databases are multi-megabyte protobuf files, so
	// anything tiny is a corrupted leftover (an interrupted download from a
	// pre-atomic version or a failed proxy fetch) and must be re-downloaded.
	minDatFileSize = 1024
)

type GeoFileManager struct {
	baseDir string
}

func NewGeoFileManager(baseDir string) *GeoFileManager {
	if baseDir == "" {
		if wd, err := os.Getwd(); err == nil {
			baseDir = wd
		} else {
			baseDir = "."
		}
	}

	return &GeoFileManager{
		baseDir: baseDir,
	}
}

func (gfm *GeoFileManager) EnsureGeoFiles() error {
	if err := gfm.ensureFile(geoSiteFile, geoSiteURL); err != nil {
		return fmt.Errorf("failed to ensure geosite.dat: %v", err)
	}

	if err := gfm.ensureFile(geoIPFile, geoIPURL); err != nil {
		return fmt.Errorf("failed to ensure geoip.dat: %v", err)
	}

	return nil
}

func (gfm *GeoFileManager) ensureFile(filename, url string) error {
	filePath := filepath.Join(gfm.baseDir, filename)

	if info, err := os.Stat(filePath); err == nil {
		if info.Size() >= minDatFileSize {
			return nil
		}
		logger.Warn("%s is only %d bytes (corrupted?), re-downloading", filename, info.Size())
	}

	logger.Info("Downloading %s...", filename)

	fileDir := filepath.Dir(filePath)
	if err := os.MkdirAll(fileDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %v (bind-mounted Docker volume? fix ownership on the host: sudo chown -R 1000:1000 %s)", fileDir, err, fileDir)
	}

	if err := gfm.downloadFile(url, filePath); err != nil {
		return fmt.Errorf("failed to download %s: %v (bind-mounted Docker volume? fix ownership on the host: sudo chown -R 1000:1000 %s)", filename, err, fileDir)
	}

	logger.Info("Downloaded %s", filename)
	return nil
}

func (gfm *GeoFileManager) downloadFile(url, filePath string) error {
	client := &http.Client{
		Timeout:   90 * time.Second,
		Transport: config.GetBootstrapTransport(),
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP request failed with status: %d", resp.StatusCode)
	}

	// Write to a temp file and rename into place so an interrupted download
	// (network drop, OOM kill, container restart) never leaves a truncated
	// .dat behind: os.Stat would treat it as a valid file on later starts
	// and Xray would fail to parse it.
	tmpPath := filePath + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}

	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write file: %v", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close file: %v", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to move file into place: %v", err)
	}

	return nil
}

// UpdateGeoFiles attempts to update geosite.dat and geoip.dat in the background.
// If an update fails, it logs a warning and keeps the existing files intact.
func (gfm *GeoFileManager) UpdateGeoFiles() {
	targets := []struct {
		filename string
		url      string
	}{
		{geoSiteFile, geoSiteURL},
		{geoIPFile, geoIPURL},
	}
	for _, t := range targets {
		filePath := filepath.Join(gfm.baseDir, t.filename)
		if err := gfm.downloadFile(t.url, filePath); err != nil {
			logger.Warn("Background update of %s failed (keeping current version): %v", t.filename, err)
		} else {
			logger.Info("Successfully updated %s", t.filename)
		}
	}
}
