package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AdminPassword          string
	AdminEmail             string
	PublicURL              string
	DefaultInstanceQuota   int
	SMTP                   SMTPConfig
	MaxSessions            int
	SessionIdleTimeout     time.Duration
	DataDir                string
	SessionHostDataDir     string
	MaxUploadSize          int64
	SessionNanoCPUs        int64
	SessionMemoryBytes     int64
	SessionShmBytes        int64
	HTTPAddr               string
	CookieSecret           string
	WebRTCICEIP            string
	ScreenWidth            string
	ScreenHeight           string
	ScreenDepth            string
	ChromeWindowTop        string
	ChromeWindowBottom     string
	WebRTCWidth            string
	WebRTCHeight           string
	WebRTCFramerate        string
	WebRTCVideoCodec       string
	WebRTCVideoBitrate     string
	WebRTCAudioBitrate     string
	PublishSessionTCPPorts bool
	DockerNetworkName      string
}

type SMTPConfig struct {
	Enabled    bool
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	Encryption string
	Timeout    time.Duration
}

func (s SMTPConfig) Configured() bool {
	return s.Enabled || s.Host != "" || s.Username != "" || s.Password != "" || s.From != ""
}

func Load() (*Config, error) {
	adminPass := os.Getenv("RB_ADMIN_PASSWORD")

	maxSessions, _ := strconv.Atoi(os.Getenv("RB_MAX_SESSIONS"))
	if maxSessions <= 0 {
		maxSessions = 5
	}

	if os.Getenv("RB_SESSION_IDLE_TIMEOUT") != "" {
		log.Print("RB_SESSION_IDLE_TIMEOUT is deprecated and ignored; browser instances remain persistent")
	}
	var err error
	quota := maxSessions
	if raw, present := os.LookupEnv("RB_DEFAULT_INSTANCE_QUOTA"); present {
		quota, err = strconv.Atoi(raw)
		if err != nil || quota < 0 {
			return nil, fmt.Errorf("RB_DEFAULT_INSTANCE_QUOTA must be a nonnegative integer")
		}
	}

	dataDir := os.Getenv("RB_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if !filepath.IsAbs(dataDir) {
		var err error
		dataDir, err = filepath.Abs(dataDir)
		if err != nil {
			return nil, fmt.Errorf("resolve data dir: %w", err)
		}
	}

	sessionHostDataDir := os.Getenv("RB_SESSION_HOST_DATA_DIR")
	if sessionHostDataDir == "" {
		sessionHostDataDir = dataDir
	}
	if !filepath.IsAbs(sessionHostDataDir) {
		var err error
		sessionHostDataDir, err = filepath.Abs(sessionHostDataDir)
		if err != nil {
			return nil, fmt.Errorf("resolve session host data dir: %w", err)
		}
	}

	maxUpload, _ := strconv.ParseInt(os.Getenv("RB_MAX_UPLOAD_SIZE"), 10, 64)
	if maxUpload <= 0 {
		maxUpload = 100 * 1024 * 1024 // 100MB
	}

	sessionCPUs, _ := strconv.ParseFloat(os.Getenv("RB_SESSION_CPUS"), 64)
	if sessionCPUs <= 0 {
		sessionCPUs = 2
	}
	sessionMemoryMB, _ := strconv.ParseInt(os.Getenv("RB_SESSION_MEMORY_MB"), 10, 64)
	if sessionMemoryMB <= 0 {
		sessionMemoryMB = 3072
	}
	sessionShmMB, _ := strconv.ParseInt(os.Getenv("RB_SESSION_SHM_MB"), 10, 64)
	if sessionShmMB <= 0 {
		sessionShmMB = 1024
	}

	httpAddr := os.Getenv("RB_HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}

	cookieSecret := os.Getenv("RB_COOKIE_SECRET")
	if cookieSecret == "" {
		cookieSecret, err = persistentSecret(dataDir)
		if err != nil {
			return nil, err
		}
	}
	if len(cookieSecret) < 32 {
		return nil, fmt.Errorf("RB_COOKIE_SECRET must contain at least 32 bytes")
	}
	smtpPort := 587
	smtpEnabled := false
	for _, key := range []string{"RB_SMTP_HOST", "RB_SMTP_PORT", "RB_SMTP_USERNAME", "RB_SMTP_PASSWORD", "RB_SMTP_FROM", "RB_SMTP_ENCRYPTION"} {
		if os.Getenv(key) != "" {
			smtpEnabled = true
		}
	}
	if raw := os.Getenv("RB_SMTP_PORT"); raw != "" {
		smtpPort, _ = strconv.Atoi(raw)
	}
	encryption := strings.ToLower(strings.TrimSpace(os.Getenv("RB_SMTP_ENCRYPTION")))
	if encryption == "" {
		encryption = "starttls"
	}
	smtpTimeout := 10 * time.Second
	if raw := os.Getenv("RB_SMTP_TIMEOUT"); raw != "" {
		smtpTimeout, err = time.ParseDuration(raw)
		if err != nil || smtpTimeout <= 0 || smtpTimeout > time.Minute {
			return nil, fmt.Errorf("RB_SMTP_TIMEOUT must be positive and at most 1m")
		}
	}

	webrtcICEIP := os.Getenv("RB_WEBRTC_ICE_IP")
	if webrtcICEIP == "" {
		webrtcICEIP = "127.0.0.1"
	}
	screenWidth := os.Getenv("RB_SCREEN_WIDTH")
	if screenWidth == "" {
		screenWidth = "1280"
	}
	screenHeight := os.Getenv("RB_SCREEN_HEIGHT")
	if screenHeight == "" {
		screenHeight = "752"
	}
	screenDepth := os.Getenv("RB_SCREEN_DEPTH")
	if screenDepth == "" {
		screenDepth = "24"
	}
	chromeWindowTop := os.Getenv("RB_CHROME_WINDOW_TOP")
	if chromeWindowTop == "" {
		chromeWindowTop = "32"
	}
	chromeWindowBottom := os.Getenv("RB_CHROME_WINDOW_BOTTOM")
	if chromeWindowBottom == "" {
		chromeWindowBottom = "0"
	}
	webrtcWidth := os.Getenv("RB_WEBRTC_WIDTH")
	if webrtcWidth == "" {
		webrtcWidth = "1280"
	}
	webrtcHeight := os.Getenv("RB_WEBRTC_HEIGHT")
	if webrtcHeight == "" {
		webrtcHeight = "720"
	}
	webrtcFramerate := os.Getenv("RB_WEBRTC_FRAMERATE")
	if webrtcFramerate == "" {
		webrtcFramerate = "24"
	}
	webrtcVideoCodec := os.Getenv("RB_WEBRTC_VIDEO_CODEC")
	if webrtcVideoCodec == "" {
		webrtcVideoCodec = "vp8"
	}
	webrtcVideoBitrate := os.Getenv("RB_WEBRTC_VIDEO_BITRATE")
	if webrtcVideoBitrate == "" {
		webrtcVideoBitrate = "1800k"
	}
	webrtcAudioBitrate := os.Getenv("RB_WEBRTC_AUDIO_BITRATE")
	if webrtcAudioBitrate == "" {
		webrtcAudioBitrate = "96k"
	}
	publishSessionTCPPorts := true
	if value := os.Getenv("RB_PUBLISH_SESSION_TCP_PORTS"); value != "" {
		publishSessionTCPPorts, err = strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid RB_PUBLISH_SESSION_TCP_PORTS: %w", err)
		}
	}
	dockerNetworkName := os.Getenv("RB_DOCKER_NETWORK")
	if dockerNetworkName == "" {
		dockerNetworkName = "remotebrowser"
	}

	return &Config{
		AdminPassword:          adminPass,
		AdminEmail:             strings.ToLower(strings.TrimSpace(os.Getenv("RB_ADMIN_EMAIL"))),
		PublicURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("RB_PUBLIC_URL")), "/"),
		DefaultInstanceQuota:   quota,
		SMTP:                   SMTPConfig{Enabled: smtpEnabled, Host: strings.TrimSpace(os.Getenv("RB_SMTP_HOST")), Port: smtpPort, Username: os.Getenv("RB_SMTP_USERNAME"), Password: os.Getenv("RB_SMTP_PASSWORD"), From: strings.TrimSpace(os.Getenv("RB_SMTP_FROM")), Encryption: encryption, Timeout: smtpTimeout},
		MaxSessions:            maxSessions,
		SessionIdleTimeout:     0, // Retained only for compatibility; never drives lifecycle.
		DataDir:                dataDir,
		SessionHostDataDir:     sessionHostDataDir,
		MaxUploadSize:          maxUpload,
		SessionNanoCPUs:        int64(sessionCPUs * 1e9),
		SessionMemoryBytes:     sessionMemoryMB << 20,
		SessionShmBytes:        sessionShmMB << 20,
		HTTPAddr:               httpAddr,
		CookieSecret:           cookieSecret,
		WebRTCICEIP:            webrtcICEIP,
		ScreenWidth:            screenWidth,
		ScreenHeight:           screenHeight,
		ScreenDepth:            screenDepth,
		ChromeWindowTop:        chromeWindowTop,
		ChromeWindowBottom:     chromeWindowBottom,
		WebRTCWidth:            webrtcWidth,
		WebRTCHeight:           webrtcHeight,
		WebRTCFramerate:        webrtcFramerate,
		WebRTCVideoCodec:       webrtcVideoCodec,
		WebRTCVideoBitrate:     webrtcVideoBitrate,
		WebRTCAudioBitrate:     webrtcAudioBitrate,
		PublishSessionTCPPorts: publishSessionTCPPorts,
		DockerNetworkName:      dockerNetworkName,
	}, nil
}

// The fallback signing key survives restarts and password changes. It is never
// derived from a credential and is readable only by the deployment user.
func persistentSecret(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("create secret directory: %w", err)
	}
	path := filepath.Join(dataDir, "cookie-secret")
	if value, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(value)), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(value)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		existing, e := os.ReadFile(path)
		return strings.TrimSpace(string(existing)), e
	}
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString(encoded + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return encoded, nil
}
