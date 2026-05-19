package codex

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
)

type codexRuntimeDefaults struct {
	ProviderName     string
	IsCurrent        bool
	Model            string
	ReasoningEffort  string
	ModelProvider    string
	BaseURL          string
	APIKey           string
	WireAPI          string
	HTTPHeaders      map[string]string
	MigrationTargets []string
}

func readCodexRuntimeDefaults(codexHome string) codexRuntimeDefaults {
	local := readCodexConfigRuntime(codexHome)
	current := codexRuntimeDefaults{}
	for _, cfg := range readCCSwitchCodexRuntimes() {
		if cfg.IsCurrent {
			current = cfg
			break
		}
	}
	if current.ProviderName == "" {
		return local
	}
	current.fillMissing(local)
	return current
}

func (d *codexRuntimeDefaults) fillMissing(other codexRuntimeDefaults) {
	if strings.TrimSpace(d.Model) == "" {
		d.Model = other.Model
	}
	if strings.TrimSpace(d.ReasoningEffort) == "" {
		d.ReasoningEffort = other.ReasoningEffort
	}
	if strings.TrimSpace(d.ModelProvider) == "" {
		d.ModelProvider = other.ModelProvider
	}
	if strings.TrimSpace(d.BaseURL) == "" {
		d.BaseURL = other.BaseURL
	}
	if strings.TrimSpace(d.APIKey) == "" {
		d.APIKey = other.APIKey
	}
	if strings.TrimSpace(d.WireAPI) == "" {
		d.WireAPI = other.WireAPI
	}
	if len(d.HTTPHeaders) == 0 {
		d.HTTPHeaders = other.HTTPHeaders
	}
	if len(d.MigrationTargets) == 0 {
		d.MigrationTargets = other.MigrationTargets
	}
}

func readCodexConfigModelOptions(codexHome string) []core.ModelOption {
	var models []core.ModelOption
	current := codexRuntimeDefaults{}
	for _, cfg := range readCCSwitchCodexRuntimes() {
		if cfg.IsCurrent {
			current = cfg
			break
		}
	}
	if strings.TrimSpace(current.Model) != "" {
		models = appendUniqueModelOptions(models, core.ModelOption{Name: strings.TrimSpace(current.Model)})
	}
	for _, target := range current.MigrationTargets {
		models = appendUniqueModelOptions(models, core.ModelOption{Name: target})
	}

	local := readCodexConfigRuntime(codexHome)
	if strings.TrimSpace(local.Model) != "" {
		models = appendUniqueModelOptions(models, core.ModelOption{Name: strings.TrimSpace(local.Model)})
	}
	for _, target := range local.MigrationTargets {
		models = appendUniqueModelOptions(models, core.ModelOption{Name: target})
	}
	return models
}

func readCodexConfigRuntime(codexHome string) codexRuntimeDefaults {
	home := resolveCodexHomeDir(codexHome)
	if home == "" {
		return codexRuntimeDefaults{}
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return codexRuntimeDefaults{}
	}
	return parseCodexRuntimeConfigString(string(b))
}

func parseCodexRuntimeConfigString(cfgStr string) codexRuntimeDefaults {
	var raw map[string]any
	if _, err := toml.Decode(cfgStr, &raw); err == nil {
		return parseCodexRuntimeConfigMap(raw)
	}
	return parseCodexRuntimeConfigLines(cfgStr)
}

func parseCodexRuntimeConfigMap(raw map[string]any) codexRuntimeDefaults {
	cfg := codexRuntimeDefaults{
		Model:           strings.TrimSpace(anyStringValue(raw["model"])),
		ReasoningEffort: normalizeReasoningEffort(anyStringValue(raw["model_reasoning_effort"])),
		ModelProvider:   strings.TrimSpace(anyStringValue(raw["model_provider"])),
	}

	providers := mapValue(raw["model_providers"])
	if len(providers) > 0 {
		providerName := cfg.ModelProvider
		if providerName == "" && len(providers) == 1 {
			for name := range providers {
				providerName = name
			}
		}
		if providerName != "" {
			if provider := mapValue(providers[providerName]); len(provider) > 0 {
				cfg.ModelProvider = providerName
				cfg.BaseURL = strings.TrimSpace(anyStringValue(provider["base_url"]))
				cfg.WireAPI = strings.TrimSpace(anyStringValue(provider["wire_api"]))
				cfg.HTTPHeaders = stringMapValue(provider["http_headers"])
			} else {
				for name, rawProvider := range providers {
					provider := mapValue(rawProvider)
					if len(provider) == 0 {
						continue
					}
					if providerName != strings.TrimSpace(anyStringValue(provider["name"])) {
						continue
					}
					cfg.ModelProvider = name
					cfg.BaseURL = strings.TrimSpace(anyStringValue(provider["base_url"]))
					cfg.WireAPI = strings.TrimSpace(anyStringValue(provider["wire_api"]))
					cfg.HTTPHeaders = stringMapValue(provider["http_headers"])
					break
				}
			}
		}
	}

	if notice := mapValue(raw["notice"]); len(notice) > 0 {
		if migrations := mapValue(notice["model_migrations"]); len(migrations) > 0 {
			for _, v := range migrations {
				if target := strings.TrimSpace(anyStringValue(v)); target != "" {
					cfg.MigrationTargets = append(cfg.MigrationTargets, target)
				}
			}
		}
	}
	return cfg
}

func parseCodexRuntimeConfigLines(cfgStr string) codexRuntimeDefaults {
	cfg := codexRuntimeDefaults{}
	section := ""
	for _, line := range strings.Split(cfgStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		key, value, ok := parseSimpleTOMLKV(line)
		if !ok {
			continue
		}
		switch {
		case section == "":
			switch key {
			case "model":
				cfg.Model = value
			case "model_reasoning_effort":
				cfg.ReasoningEffort = normalizeReasoningEffort(value)
			case "model_provider":
				cfg.ModelProvider = value
			case "base_url":
				cfg.BaseURL = value
			case "wire_api":
				cfg.WireAPI = value
			}
		case strings.HasPrefix(section, "model_providers."):
			providerName := strings.TrimPrefix(section, "model_providers.")
			if cfg.ModelProvider == "" || providerName == cfg.ModelProvider {
				cfg.ModelProvider = providerName
				switch key {
				case "base_url":
					cfg.BaseURL = value
				case "wire_api":
					cfg.WireAPI = value
				}
			}
		case section == "notice.model_migrations":
			cfg.MigrationTargets = append(cfg.MigrationTargets, value)
		}
	}
	return cfg
}

func parseSimpleTOMLKV(line string) (key, value string, ok bool) {
	idx := strings.Index(line, "=")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	value = strings.Trim(value, "\"'")
	return key, value, key != ""
}

func readCCSwitchCodexRuntimes() []codexRuntimeDefaults {
	dbPath := findCCSwitchDBForCodex()
	if dbPath == "" {
		return nil
	}
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name, settings_config, is_current FROM providers WHERE app_type = 'codex' ORDER BY is_current DESC, name COLLATE NOCASE`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []codexRuntimeDefaults
	for rows.Next() {
		var name, settings string
		var isCurrent int
		if err := rows.Scan(&name, &settings, &isCurrent); err != nil {
			continue
		}
		cfg := parseCCSwitchCodexRuntime(name, settings)
		cfg.ProviderName = strings.TrimSpace(name)
		cfg.IsCurrent = isCurrent == 1
		result = append(result, cfg)
	}
	return result
}

func parseCCSwitchCodexRuntime(name, settings string) codexRuntimeDefaults {
	cfg := codexRuntimeDefaults{ProviderName: strings.TrimSpace(name)}
	var sc map[string]any
	if err := json.Unmarshal([]byte(settings), &sc); err != nil {
		return cfg
	}
	if auth := mapValue(sc["auth"]); len(auth) > 0 {
		cfg.APIKey = strings.TrimSpace(anyStringValue(auth["OPENAI_API_KEY"]))
	}
	if env := mapValue(sc["env"]); len(env) > 0 {
		if cfg.APIKey == "" {
			cfg.APIKey = strings.TrimSpace(anyStringValue(env["OPENAI_API_KEY"]))
		}
		cfg.BaseURL = strings.TrimSpace(anyStringValue(env["OPENAI_BASE_URL"]))
	}
	if cfgStr := anyStringValue(sc["config"]); strings.TrimSpace(cfgStr) != "" {
		fromConfig := parseCodexRuntimeConfigString(cfgStr)
		fromConfig.ProviderName = cfg.ProviderName
		fromConfig.APIKey = cfg.APIKey
		fromConfig.fillMissing(cfg)
		cfg = fromConfig
	}
	return cfg
}

func findCCSwitchDBForCodex() string {
	for _, p := range ccSwitchDBCandidatesForCodex() {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func ccSwitchDBCandidatesForCodex() []string {
	if override := strings.TrimSpace(os.Getenv("CC_SWITCH_DB_PATH")); override != "" {
		return []string{override}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{filepath.Join(home, ".cc-switch", "cc-switch.db")}
	switch runtime.GOOS {
	case "linux":
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		candidates = append(candidates, filepath.Join(dataHome, "cc-switch", "cc-switch.db"))
	case "darwin":
		candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "cc-switch", "cc-switch.db"))
	}
	return candidates
}

func anyStringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return ""
	}
}

func mapValue(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	default:
		return nil
	}
}

func stringMapValue(v any) map[string]string {
	raw := mapValue(v)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s := strings.TrimSpace(anyStringValue(v)); s != "" {
			out[k] = s
		}
	}
	return out
}

func appendUniqueModelOptions(dst []core.ModelOption, src ...core.ModelOption) []core.ModelOption {
	seen := make(map[string]struct{}, len(dst)+len(src))
	for _, m := range dst {
		if name := strings.TrimSpace(m.Name); name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, m := range src {
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			continue
		}
		if _, ok := seen[m.Name]; ok {
			continue
		}
		seen[m.Name] = struct{}{}
		dst = append(dst, m)
	}
	return dst
}

func prioritizeModelOptions(current string, models []core.ModelOption) []core.ModelOption {
	current = strings.TrimSpace(current)
	unique := appendUniqueModelOptions(nil, models...)
	if current == "" {
		return unique
	}

	var currentOpt *core.ModelOption
	rest := make([]core.ModelOption, 0, len(unique))
	for _, m := range unique {
		if m.Name == current && currentOpt == nil {
			cp := m
			currentOpt = &cp
			continue
		}
		rest = append(rest, m)
	}
	if currentOpt == nil {
		return append([]core.ModelOption{{Name: current, Desc: "current"}}, rest...)
	}
	return append([]core.ModelOption{*currentOpt}, rest...)
}

func upsertEnv(env []string, key, value string) []string {
	if strings.TrimSpace(key) == "" {
		return env
	}
	prefix := key + "="
	out := env[:0]
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
