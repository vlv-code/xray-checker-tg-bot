package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xray-checker/config"
)

const (
	DefaultGitHubRepo      = "vlv-code/xray-checker-tg-bot"
	GitHubLatestReleaseURL = "https://api.github.com/repos/vlv-code/xray-checker-tg-bot/releases/latest"
	releaseFetchTimeout    = 15 * time.Second
)

// ReleaseInfo holds details about a published GitHub release.
type ReleaseInfo struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
}

// parseSemVer parses standard semver like "v2.4.0", "2.4.1", "v2.3.5-beta".
func parseSemVer(v string) (major, minor, patch int, extra string, ok bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	if v == "" || v == "unknown" || v == "dev" {
		return 0, 0, 0, "", false
	}

	// Split off any pre-release or build metadata (e.g. -beta, +build)
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		extra = v[idx:]
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	if len(parts) == 0 {
		return 0, 0, 0, "", false
	}

	var err error
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, "", false
	}

	if len(parts) > 1 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, 0, "", false
		}
	}

	if len(parts) > 2 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return 0, 0, 0, "", false
		}
	}

	// Handle extra 4th digit if present (e.g. 2.3.5.1)
	if len(parts) > 3 {
		extra = "." + strings.Join(parts[3:], ".") + extra
	}

	return major, minor, patch, extra, true
}

// isNewerVersion returns true if latest is strictly newer than current.
func isNewerVersion(current, latest string) bool {
	if current == "" || latest == "" || current == latest {
		return false
	}

	currMaj, currMin, currPatch, currExtra, currOk := parseSemVer(current)
	if !currOk {
		return false
	}

	lateMaj, lateMin, latePatch, lateExtra, lateOk := parseSemVer(latest)
	if !lateOk {
		return false
	}

	if lateMaj > currMaj {
		return true
	}
	if lateMaj < currMaj {
		return false
	}

	if lateMin > currMin {
		return true
	}
	if lateMin < currMin {
		return false
	}

	if latePatch > currPatch {
		return true
	}
	if latePatch < currPatch {
		return false
	}

	// If numeric versions are identical, a release without -pre is newer than with -pre
	if currExtra != "" && lateExtra == "" {
		return true
	}

	// If latest has an extra numeric extension (e.g. 2.3.5.1 > 2.3.5)
	if strings.HasPrefix(lateExtra, ".") && !strings.HasPrefix(currExtra, ".") {
		return true
	}

	return false
}

// shouldAlertNewRelease checks if an alert should be sent for the latest release.
func shouldAlertNewRelease(current, latest, lastNotified string) bool {
	if latest == "" || latest == lastNotified {
		return false
	}
	return isNewerVersion(current, latest)
}

// fetchLatestReleaseFromURL fetches release information from a given endpoint.
func fetchLatestReleaseFromURL(client *http.Client, url string) (*ReleaseInfo, error) {
	if client == nil {
		client = &http.Client{
			Timeout:   releaseFetchTimeout,
			Transport: config.GetBootstrapTransport(),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), releaseFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "xray-checker-bot")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned HTTP %d", resp.StatusCode)
	}

	var rel ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decoding release JSON: %w", err)
	}

	return &rel, nil
}

// FetchLatestRelease fetches the latest release from the official GitHub repo.
func FetchLatestRelease() (*ReleaseInfo, error) {
	client := &http.Client{
		Timeout:   releaseFetchTimeout,
		Transport: config.GetBootstrapTransport(),
	}
	return fetchLatestReleaseFromURL(client, GitHubLatestReleaseURL)
}

// formatReleaseNotification builds an HTML notification message about a new release.
func formatReleaseNotification(currentVersion string, rel *ReleaseInfo) string {
	var sb strings.Builder

	sb.WriteString("🚀 <b>Доступно обновление Xray Checker!</b>\n\n")

	sb.WriteString(fmt.Sprintf("• Текущая версия: <code>%s</code>\n", escapeHTML(currentVersion)))
	sb.WriteString(fmt.Sprintf("• Новая версия: <code>%s</code>\n\n", escapeHTML(rel.TagName)))

	title := rel.Name
	if title == "" {
		title = rel.TagName
	}
	sb.WriteString(fmt.Sprintf("📦 <b>Релиз:</b> <a href=\"%s\">%s</a>\n", rel.HTMLURL, escapeHTML(title)))

	body := strings.TrimSpace(rel.Body)
	if body != "" {
		// Truncate changelog if overly verbose (up to 400 chars)
		if len([]rune(body)) > 400 {
			runes := []rune(body)
			body = string(runes[:397]) + "..."
		}
		sb.WriteString(fmt.Sprintf("\n<b>Что нового:</b>\n<blockquote>%s</blockquote>\n", escapeHTML(body)))
	}

	sb.WriteString("\n<i>Для обновления выполните pull нового docker-образа или перезапустите службу.</i>")

	return sb.String()
}
