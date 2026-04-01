package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	pb "github.com/nezhahq/agent/proto"
)

var errNotEmbyTarget = errors.New("not an emby target")

type embyTargetConfig struct {
	Type            string `json:"type"`
	URL             string `json:"url"`
	Endpoint        string `json:"endpoint"`
	ExpectedName    string `json:"expected_name"`
	ExpectedVersion string `json:"expected_version"`
	ExpectedID      string `json:"expected_id"`
}

type embyPublicInfo struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
}

func parseEmbyTarget(raw string) (*embyTargetConfig, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return nil, errNotEmbyTarget
	}

	if strings.HasPrefix(target, "{") {
		var cfg embyTargetConfig
		if err := json.Unmarshal([]byte(target), &cfg); err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(cfg.Type), "emby") {
			return nil, errNotEmbyTarget
		}
		cfg.URL = strings.TrimSpace(cfg.URL)
		cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
		cfg.ExpectedName = strings.TrimSpace(cfg.ExpectedName)
		cfg.ExpectedVersion = strings.TrimSpace(cfg.ExpectedVersion)
		cfg.ExpectedID = strings.TrimSpace(cfg.ExpectedID)
		if cfg.URL == "" {
			return nil, errors.New("emby target url is required")
		}
		return &cfg, nil
	}

	if strings.HasPrefix(target, "emby+http://") || strings.HasPrefix(target, "emby+https://") {
		target = strings.TrimPrefix(target, "emby+")
		return &embyTargetConfig{Type: "emby", URL: target}, nil
	}

	if strings.HasPrefix(target, "emby://") {
		target = strings.TrimPrefix(target, "emby://")
		if !strings.Contains(target, "://") {
			target = "https://" + target
		}
		u, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		cfg := &embyTargetConfig{
			Type:            "emby",
			URL:             (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String(),
			Endpoint:        strings.TrimSpace(q.Get("endpoint")),
			ExpectedName:    strings.TrimSpace(q.Get("name")),
			ExpectedVersion: strings.TrimSpace(q.Get("version")),
			ExpectedID:      strings.TrimSpace(q.Get("id")),
		}
		if cfg.URL == "" {
			return nil, errors.New("emby target url is required")
		}
		return cfg, nil
	}

	return nil, errNotEmbyTarget
}

func joinURL(baseURL, endpoint string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if base.Scheme == "" {
		base.Scheme = "https"
	}
	if base.Host == "" && base.Path != "" {
		base.Host = base.Path
		base.Path = ""
	}
	if base.Host == "" {
		return "", errors.New("missing emby host")
	}
	ref, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func buildEmbyCandidates(cfg *embyTargetConfig) ([]string, error) {
	if cfg == nil {
		return nil, errors.New("nil emby config")
	}
	if cfg.Endpoint != "" {
		target, err := joinURL(cfg.URL, cfg.Endpoint)
		if err != nil {
			return nil, err
		}
		return []string{target}, nil
	}

	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}
	if base.Scheme == "" {
		base.Scheme = "https"
	}
	if base.Host == "" && base.Path != "" {
		base.Host = base.Path
		base.Path = ""
	}
	if base.Host == "" {
		return nil, errors.New("missing emby host")
	}
	if strings.HasSuffix(base.Path, "/System/Info/Public") || strings.HasSuffix(base.Path, "/emby/System/Info/Public") {
		return []string{base.String()}, nil
	}

	root := (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/"}).String()
	candidates := []string{}
	if base.Path != "" && base.Path != "/" {
		if u, err := joinURL(cfg.URL, "System/Info/Public"); err == nil {
			candidates = append(candidates, u)
		}
	}
	for _, endpoint := range []string{"/System/Info/Public", "/emby/System/Info/Public"} {
		u, err := joinURL(root, endpoint)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, u)
	}
	return dedupeStrings(candidates), nil
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateEmbyInfo(cfg *embyTargetConfig, info *embyPublicInfo) error {
	if cfg.ExpectedName != "" && info.ServerName != cfg.ExpectedName {
		return fmt.Errorf("emby server name mismatch: expected %q, got %q", cfg.ExpectedName, info.ServerName)
	}
	if cfg.ExpectedVersion != "" && info.Version != cfg.ExpectedVersion {
		return fmt.Errorf("emby version mismatch: expected %q, got %q", cfg.ExpectedVersion, info.Version)
	}
	if cfg.ExpectedID != "" && info.ID != cfg.ExpectedID {
		return fmt.Errorf("emby id mismatch: expected %q, got %q", cfg.ExpectedID, info.ID)
	}
	return nil
}

func handleEmbyProbeTask(task *pb.Task, result *pb.TaskResult, cfg *embyTargetConfig) {
	candidates, err := buildEmbyCandidates(cfg)
	if err != nil {
		result.Data = err.Error()
		return
	}

	start := time.Now()
	var lastErr error
	for _, targetURL := range candidates {
		printf("HTTP-GET Emby Task: %s", targetURL)
		resp, err := httpClient.Get(targetURL)
		if err != nil {
			lastErr = err
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode > http.StatusIMUsed {
			lastErr = fmt.Errorf("emby http status: %s", resp.Status)
			continue
		}

		var info embyPublicInfo
		if err := json.Unmarshal(body, &info); err != nil {
			lastErr = fmt.Errorf("emby response is not valid json: %w", err)
			continue
		}
		if info.ServerName == "" && info.Version == "" && info.ID == "" {
			lastErr = errors.New("emby response missing public info fields")
			continue
		}
		if err := validateEmbyInfo(cfg, &info); err != nil {
			lastErr = err
			continue
		}

		result.Delay = float32(time.Since(start).Microseconds()) / 1000.0
		result.Successful = true
		result.Data = fmt.Sprintf("emby|%s|%s|%s", info.ServerName, info.Version, info.ID)
		return
	}

	if lastErr != nil {
		result.Data = lastErr.Error()
	}
}
