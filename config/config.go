package config

import (
	"fmt"
	"os"

	"xray-checker/nodes"

	"github.com/alecthomas/kong"
)

var CLIConfig CLI
var Version string

func Parse(version string) {
	Version = version
	ctx := kong.Parse(&CLIConfig,
		kong.Name("xray-checker"),
		kong.Description("Xray Checker: A Prometheus exporter for monitoring Xray proxies"),
		kong.Vars{
			"version": version,
		},
	)
	_ = ctx
	if err := CLIConfig.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
}

type CLI struct {
	Subscription struct {
		URLs           []string `name:"subscription-url" help:"URL(s) of the subscription (can be specified multiple times)" env:"SUBSCRIPTION_URL"`
		Update         bool     `name:"subscription-update" help:"Whether to recheck the subscription" default:"true" env:"SUBSCRIPTION_UPDATE"`
		UpdateInterval int      `name:"subscription-update-interval" help:"Interval for subscription updates in seconds" default:"300" env:"SUBSCRIPTION_UPDATE_INTERVAL"`
		JSONFormat     bool     `name:"subscription-json-format" help:"Request full JSON configs from the panel (sends app-like headers so grouped/balancer nodes are returned individually instead of collapsed share links)" default:"false" env:"SUBSCRIPTION_JSON_FORMAT"`
		UserAgent      string   `name:"subscription-user-agent" help:"Custom User-Agent for subscription requests (overrides the default and the --subscription-json-format preset)" default:"" env:"SUBSCRIPTION_USER_AGENT"`
		Headers        []string `name:"subscription-header" help:"Extra HTTP header for subscription requests in 'Key: Value' form (repeatable; env: comma-separated)" env:"SUBSCRIPTION_HEADERS"`
		StorePath      string   `name:"subscription-store-path" help:"File to persist subscriptions added via the Telegram bot's /addsub (empty disables persistence, added subscriptions won't survive a restart)" default:"subscriptions.json" env:"SUBSCRIPTION_STORE_PATH"`
		CachePath      string   `name:"subscription-cache-path" help:"File to cache fetched proxy configurations for offline/degraded startup" default:"proxy_cache.json" env:"SUBSCRIPTION_CACHE_PATH"`
	} `embed:"" prefix:""`

	Proxy struct {
		CheckInterval    int    `name:"proxy-check-interval" help:"Interval for proxy checks in seconds" default:"300" env:"PROXY_CHECK_INTERVAL"`
		CheckConcurrency int    `name:"proxy-check-concurrency" help:"Max proxies checked in parallel per cycle (0 = unlimited)" default:"0" env:"PROXY_CHECK_CONCURRENCY"`
		CheckMethod      string `name:"proxy-check-method" help:"Method for checking proxy, ip, status or download" default:"ip" env:"PROXY_CHECK_METHOD"`
		IpCheckUrl       string `name:"proxy-ip-check-url" help:"Service URL for IP checking" default:"https://api.ipify.org?format=text" env:"PROXY_IP_CHECK_URL"`
		StatusCheckUrl   string `name:"proxy-status-check-url" help:"Response status generator, used by check-method=status" default:"http://cp.cloudflare.com/generate_204" env:"PROXY_STATUS_CHECK_URL"`
		DownloadUrl      string `name:"proxy-download-url" help:"URL for file download checking, used by check-method=download" default:"https://proof.ovh.net/files/1Mb.dat" env:"PROXY_DOWNLOAD_URL"`
		DownloadTimeout  int    `name:"proxy-download-timeout" help:"Timeout for download checking in seconds" default:"60" env:"PROXY_DOWNLOAD_TIMEOUT"`
		DownloadMinSize  int64  `name:"proxy-download-min-size" help:"Minimum bytes to download for successful check" default:"51200" env:"PROXY_DOWNLOAD_MIN_SIZE"`
		Timeout          int    `name:"proxy-timeout" help:"Timeout for IP checking in seconds" default:"30" env:"PROXY_TIMEOUT"`
		SimulateLatency  bool   `name:"simulate-latency" help:"Whether to add latency to the response" default:"true" env:"SIMULATE_LATENCY"`
		ResolveDomains   bool   `name:"proxy-resolve-domains" help:"Resolve proxy server domains into IPs and expand configs" env:"PROXY_RESOLVE_DOMAINS"`
	} `embed:"" prefix:""`

	Xray struct {
		StartPort int    `name:"xray-start-port" help:"Start port for proxy configuration" default:"10000" env:"XRAY_START_PORT"`
		LogLevel  string `name:"xray-log-level" help:"Xray log level (debug|info|warning|error|none)" default:"none" env:"XRAY_LOG_LEVEL"`
	} `embed:"" prefix:""`

	Metrics struct {
		Host      string `name:"metrics-host" help:"Host to listen on" default:"0.0.0.0" env:"METRICS_HOST"`
		Port      string `name:"metrics-port" help:"Port to listen on" default:"2112" env:"METRICS_PORT"`
		Protected bool   `name:"metrics-protected" help:"Whether metrics are protected by basic auth" default:"false" env:"METRICS_PROTECTED"`
		Username  string `name:"metrics-username" help:"Username for metrics if protected by basic auth" default:"metricsUser" env:"METRICS_USERNAME"`
		Password  string `name:"metrics-password" help:"Password for metrics if protected by basic auth" default:"" env:"METRICS_PASSWORD"`
		Instance  string `name:"metrics-instance" help:"Instance label for metrics" default:"" env:"METRICS_INSTANCE"`
		PushURL   string `name:"metrics-push-url" help:"Prometheus pushgateway URL (e.g. https://user:pass@host:port)" default:"" env:"METRICS_PUSH_URL"`
		BasePath  string `name:"metrics-base-path" help:"URL path to metrics (e.g. /xray/metrics)" default:"" env:"METRICS_BASE_PATH"`
	} `embed:"" prefix:""`

	Web struct {
		Enabled             bool   `name:"web-enabled" help:"Enable web dashboard panel (default: true). Set to false to run without web UI" default:"true" env:"WEB_ENABLED"`
		ShowServerDetails   bool   `name:"web-show-details" help:"Show server IP addresses and ports in web UI" default:"false" env:"WEB_SHOW_DETAILS"`
		Public              bool   `name:"web-public" help:"Make dashboard public (requires --metrics-protected)" default:"false" env:"WEB_PUBLIC"`
		TrustedExternalAuth bool   `name:"web-trusted-external-auth" help:"Allow server details in public mode when an external auth proxy protects the dashboard" default:"false" env:"WEB_TRUSTED_EXTERNAL_AUTH"`
		PublicShowProtocol  bool   `name:"web-public-show-protocol" help:"Show protocol badge on cards in public mode (always shown when not public)" default:"false" env:"WEB_PUBLIC_SHOW_PROTOCOL"`
		CustomAssetsPath    string `name:"web-custom-assets-path" help:"Path to custom assets directory (logo.svg, favicon.ico, custom.css, index.html)" default:"" env:"WEB_CUSTOM_ASSETS_PATH"`
	} `embed:"" prefix:""`

	Telegram struct {
		BotToken                  string   `name:"telegram-bot-token" help:"Telegram bot token (from @BotFather); enables the bot when set" default:"" env:"TELEGRAM_BOT_TOKEN"`
		ChatTargets               []string `name:"telegram-chat-id" help:"Chat ID(s) allowed to use the bot and receive alerts; append :<topic_id> to target a forum topic (e.g. -100123:42)" env:"TELEGRAM_CHAT_IDS"`
		AdminUserIDs              []int64  `name:"telegram-admin-user-id" help:"Telegram user IDs allowed to issue mutating commands (/interval, /addsub, /nodeadd, etc.). If empty, all members of allowed chats can use them. env: comma-separated" env:"TELEGRAM_ADMIN_USER_IDS"`
		NotifyOnRecovery          bool     `name:"telegram-notify-on-recovery" help:"Send a message when a proxy comes back online, not just when it goes down" default:"true" env:"TELEGRAM_NOTIFY_ON_RECOVERY"`
		Commands                  bool     `name:"telegram-commands" help:"Enable interactive bot commands (/status, /help)" default:"true" env:"TELEGRAM_COMMANDS_ENABLED"`
		ManageSubscriptions       bool     `name:"telegram-manage-subscriptions" help:"Allow /addsub, /delsub and /subs so allowed chats can add or remove subscriptions at runtime" default:"true" env:"TELEGRAM_MANAGE_SUBSCRIPTIONS"`
		AlertMode                 string   `name:"telegram-alert-mode" help:"Alert mode: 'live' (edits outage message) or 'clean' (auto-deletes)" default:"clean" env:"TELEGRAM_ALERT_MODE"`
		QuietHoursEnabled         bool     `name:"telegram-quiet-hours" help:"Enable quiet hours" default:"true" env:"TELEGRAM_QUIET_HOURS_ENABLED"`
		QuietHoursStart           string   `name:"telegram-quiet-hours-start" help:"Quiet hours start time (HH:MM)" default:"23:00" env:"TELEGRAM_QUIET_HOURS_START"`
		QuietHoursEnd             string   `name:"telegram-quiet-hours-end" help:"Quiet hours end time (HH:MM)" default:"08:00" env:"TELEGRAM_QUIET_HOURS_END"`
		DayDigestEnabled          bool     `name:"telegram-day-digest" help:"Enable daytime status digests" default:"true" env:"TELEGRAM_DAY_DIGEST_ENABLED"`
		DayDigestIntervalHours    int      `name:"telegram-day-digest-interval" help:"Interval in hours between daytime digests" default:"6" env:"TELEGRAM_DAY_DIGEST_INTERVAL_HOURS"`
		StatsStorePath            string   `name:"telegram-stats-store-path" help:"Path to JSON file storing outage statistics" default:"stats.json" env:"STATS_STORE_PATH"`
		BotConfigStorePath        string   `name:"telegram-config-store-path" help:"Path to JSON file storing runtime bot configuration" default:"bot_config.json" env:"BOT_CONFIG_STORE_PATH"`
		AlertStorePath            string   `name:"telegram-alert-store-path" help:"Path to JSON file storing active outage alerts" default:"alerts.json" env:"ALERT_STORE_PATH"`
		TargetURLs                []string `name:"proxy-target-url" help:"Target URLs to check proxies against (can be specified multiple times)" env:"PROXY_TARGET_URLS"`
		RichMode                  bool     `name:"telegram-rich-mode" help:"Render reports as Telegram Bot API Rich Messages" default:"false" env:"TELEGRAM_RICH_MODE"`
		CheckHostBgEnabled        bool     `name:"checkhost-bg-enabled" help:"Enable periodic background Check-Host auditing" default:"true" env:"CHECKHOST_BG_ENABLED"`
		CheckHostIntervalHours    int      `name:"checkhost-interval-hours" help:"Interval in hours between background Check-Host audits" default:"1" env:"CHECKHOST_INTERVAL_HOURS"`
		CheckHostAlertEnabled     bool     `name:"checkhost-alert-enabled" help:"Send alert when host is unreachable from Russia in background check" default:"true" env:"CHECKHOST_ALERT_ENABLED"`
		ReleaseAlertsEnabled      bool     `name:"telegram-release-alerts" help:"Send Telegram notification when a new xray-checker release is available" default:"true" env:"TELEGRAM_RELEASE_ALERTS"`
		ReleaseCheckIntervalHours int      `name:"telegram-release-check-interval" help:"Interval in hours between release checks" default:"6" env:"TELEGRAM_RELEASE_CHECK_INTERVAL_HOURS"`
	} `embed:"" prefix:""`

	Nodes struct {
		List            []string `name:"node" help:"Remote checker node as 'name|token' (repeatable; env: comma-separated). Empty disables the node feature" env:"NODES"`
		StorePath       string   `name:"nodes-store-path" help:"File with the desired managed subscriptions per node, edited via the bot" default:"node_subs.json" env:"NODES_STORE_PATH"`
		MasterPublicURL string   `name:"master-public-url" help:"Public URL or IP of this master for nodes to report to (e.g. http://1.2.3.4:2112/api/v1/nodes/report)" default:"" env:"MASTER_PUBLIC_URL"`
		StaleTimeoutSec int      `name:"nodes-stale-timeout" help:"Master-side cap on how long a node can go silent before being marked down, in seconds. 0 = use the node's own reported check interval (backward-compatible). Caps a node claiming an unreasonably long interval." default:"0" env:"MASTER_STALE_TIMEOUT_SEC"`
	} `embed:"" prefix:""`

	Report struct {
		URL   string `name:"report-url" help:"Master ingest URL to push check snapshots to (enables node reporting when set)" default:"" env:"REPORT_URL"`
		Token string `name:"report-token" help:"Bearer token matching this node's entry in the master's NODES list" default:"" env:"REPORT_TOKEN"`
	} `embed:"" prefix:""`

	ASN struct {
		DBURL string `name:"asn-db-url" help:"URL of the gzipped ASN mmdb database (db-ip asn-lite format)" default:"https://download.db-ip.com/free/dbip-asn-lite-2026-08.mmdb.gz" env:"ASN_DB_URL"`
	} `embed:"" prefix:""`

	Version  VersionFlag `name:"version" help:"Print version information and quit"`
	RunOnce  bool        `name:"run-once" help:"Run one check cycle and exit" default:"false" env:"RUN_ONCE"`
	LogLevel string      `name:"log-level" help:"Log level (debug|info|warn|error|none)" default:"info" env:"LOG_LEVEL"`
}

func (c *CLI) Validate() error {
	checkMethod := c.Proxy.CheckMethod
	if checkMethod == "" {
		checkMethod = "ip"
	}
	if checkMethod != "ip" && checkMethod != "status" && checkMethod != "download" {
		return fmt.Errorf("invalid proxy-check-method / PROXY_CHECK_METHOD %q: must be 'ip', 'status', or 'download'", c.Proxy.CheckMethod)
	}
	if c.Web.Enabled && c.Web.Public && !c.Metrics.Protected {
		return fmt.Errorf("--web-public requires --metrics-protected to be enabled")
	}
	if c.Metrics.Protected && c.Metrics.Port != "" && c.Metrics.Port != "0" && c.Metrics.Password == "" {
		return fmt.Errorf("METRICS_PROTECTED is true but METRICS_PASSWORD is empty. Please set a secure METRICS_PASSWORD via environment variable or --metrics-password flag")
	}
	if c.Telegram.BotToken != "" && len(c.Telegram.ChatTargets) == 0 {
		return fmt.Errorf("--telegram-bot-token requires at least one --telegram-chat-id")
	}
	if len(c.Nodes.List) > 0 {
		if _, err := nodes.ParseNodes(c.Nodes.List); err != nil {
			return err
		}
		if c.Metrics.Port == "" || c.Metrics.Port == "0" {
			return fmt.Errorf("NODES requires a listening HTTP port for the ingest endpoint (METRICS_PORT is empty or 0)")
		}
	}
	if (c.Report.URL == "") != (c.Report.Token == "") {
		return fmt.Errorf("REPORT_URL and REPORT_TOKEN must be set together")
	}
	return nil
}

type VersionFlag string

func (v VersionFlag) Decode(ctx *kong.DecodeContext) error { return nil }
func (v VersionFlag) IsBool() bool                         { return true }
func (v VersionFlag) BeforeApply(app *kong.Kong, vars kong.Vars) error {
	fmt.Println("Xray Checker Bot: A Prometheus exporter and Telegram bot for monitoring Xray proxies")
	fmt.Printf("Version:\t %s\n", vars["version"])
	fmt.Printf("GitHub: https://github.com/vlv-code/xray-checker-tg-bot\n")
	app.Exit(0)
	return nil
}
