package mcp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// The mcp.* settings keys (SPEC-11 §4.3). They have no registry; consumers
// read them with the defaults below (SPEC-01 §5.5, orchestrator ruling 6).
const (
	KeyEnabled                = "mcp.enabled"
	KeyHTTPEnabled            = "mcp.http.enabled"
	KeyHTTPAddr               = "mcp.http.addr"
	KeyHTTPTokenRef           = "mcp.http.token_ref"
	KeyAccessMode             = "mcp.access_mode"
	KeyConfirmationTTLSeconds = "mcp.confirmation_ttl_seconds"
	KeyRetentionDays          = "mcp.retention_days"
)

// Defaults of the settings above.
const (
	DefaultHTTPAddr        = "127.0.0.1:39217"
	DefaultConfirmationTTL = 900 * time.Second
	MinConfirmationTTL     = 60 * time.Second
	MaxConfirmationTTL     = 86400 * time.Second
	DefaultRetentionDays   = 30
	MinRetentionDays       = 1
	MaxRetentionDays       = 3650
)

// SettingsValues is the resolved mcp.* configuration.
type SettingsValues struct {
	Enabled         bool
	HTTPEnabled     bool
	HTTPAddr        string
	HTTPTokenRef    string
	AccessMode      string
	ConfirmationTTL time.Duration
	RetentionDays   int
}

// DefaultSettings are the consumer-side defaults.
func DefaultSettings() SettingsValues {
	return SettingsValues{
		Enabled:         true,
		HTTPEnabled:     false,
		HTTPAddr:        DefaultHTTPAddr,
		AccessMode:      AccessReadWrite,
		ConfirmationTTL: DefaultConfirmationTTL,
		RetentionDays:   DefaultRetentionDays,
	}
}

// LoadSettings resolves the mcp.* keys, falling back to DefaultSettings.
func LoadSettings(ctx context.Context, store SettingStore) (SettingsValues, error) {
	v := DefaultSettings()
	if store == nil {
		return v, nil
	}
	var err error
	if v.Enabled, err = boolSetting(ctx, store, KeyEnabled, v.Enabled); err != nil {
		return v, err
	}
	if v.HTTPEnabled, err = boolSetting(ctx, store, KeyHTTPEnabled, v.HTTPEnabled); err != nil {
		return v, err
	}
	if v.HTTPAddr, err = stringSetting(ctx, store, KeyHTTPAddr, v.HTTPAddr); err != nil {
		return v, err
	}
	if err := ValidateListenAddr(v.HTTPAddr); err != nil {
		return v, fmt.Errorf("%s: %w", KeyHTTPAddr, err)
	}
	if v.HTTPTokenRef, err = stringSetting(ctx, store, KeyHTTPTokenRef, v.HTTPTokenRef); err != nil {
		return v, err
	}
	if v.AccessMode, err = stringSetting(ctx, store, KeyAccessMode, v.AccessMode); err != nil {
		return v, err
	}
	switch v.AccessMode {
	case AccessReadOnly, AccessReadWrite:
	default:
		return v, fmt.Errorf("%s: 取值必须是 %s 或 %s", KeyAccessMode, AccessReadOnly, AccessReadWrite)
	}
	ttl, err := intSetting(ctx, store, KeyConfirmationTTLSeconds, int(DefaultConfirmationTTL/time.Second))
	if err != nil {
		return v, err
	}
	if time.Duration(ttl)*time.Second < MinConfirmationTTL || time.Duration(ttl)*time.Second > MaxConfirmationTTL {
		return v, fmt.Errorf("%s: 取值范围 %d–%d", KeyConfirmationTTLSeconds, int(MinConfirmationTTL/time.Second), int(MaxConfirmationTTL/time.Second))
	}
	v.ConfirmationTTL = time.Duration(ttl) * time.Second
	days, err := intSetting(ctx, store, KeyRetentionDays, DefaultRetentionDays)
	if err != nil {
		return v, err
	}
	if days < MinRetentionDays || days > MaxRetentionDays {
		return v, fmt.Errorf("%s: 取值范围 %d–%d", KeyRetentionDays, MinRetentionDays, MaxRetentionDays)
	}
	v.RetentionDays = days
	return v, nil
}

func stringSetting(ctx context.Context, s SettingStore, key, def string) (string, error) {
	raw, ok, err := s.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("读取设置 %s: %w", key, err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	return raw, nil
}

func boolSetting(ctx context.Context, s SettingStore, key string, def bool) (bool, error) {
	raw, ok, err := s.Get(ctx, key)
	if err != nil {
		return def, fmt.Errorf("读取设置 %s: %w", key, err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return def, fmt.Errorf("%s: 取值必须是布尔值", key)
}

func intSetting(ctx context.Context, s SettingStore, key string, def int) (int, error) {
	raw, ok, err := s.Get(ctx, key)
	if err != nil {
		return def, fmt.Errorf("读取设置 %s: %w", key, err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def, fmt.Errorf("%s: 取值必须是整数", key)
	}
	return n, nil
}

// ValidateListenAddr enforces the loopback-only rule of §5.8: the host part
// must be 127.0.0.1 or ::1. 0.0.0.0 and empty hosts are rejected, and so are
// hostnames, because a hostname can resolve off-box.
func ValidateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址必须形如 127.0.0.1:端口")
	}
	if !IsLoopbackHost(host) {
		return fmt.Errorf("只允许绑定回环地址 127.0.0.1 或 ::1，收到的 %q 被拒绝", host)
	}
	if port == "" {
		return fmt.Errorf("必须指定端口")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("端口必须在 0–65535 之间")
	}
	return nil
}

// IsLoopbackHost reports whether host is a literal loopback address.
func IsLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "::1":
		return true
	}
	return false
}
