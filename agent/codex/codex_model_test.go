package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestConfiguredModels_BoundaryConditions(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "first"}}},
			{Models: []core.ModelOption{{Name: "second"}}},
		},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "out of range", activeIdx: 2, wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.activeIdx = tt.activeIdx
			got := a.configuredModels()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("configuredModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("configuredModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestGetModel_PrefersActiveProviderModel(t *testing.T) {
	a := &Agent{
		model: "gpt-4.1-mini",
		providers: []core.ProviderConfig{
			{Name: "openai", Model: "gpt-5.4"},
		},
		activeIdx: 0,
	}

	if got := a.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want gpt-5.4", got)
	}
}

func TestGetModel_PrefersCCSwitchCurrentWhenNoRuntimeOverride(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, ".codex")
	writeTestCodexConfig(t, codexHome, "gpt-5.4", "xhigh")
	dbPath := writeTestCCSwitchDB(t, tmp)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CC_SWITCH_DB_PATH", dbPath)

	a := &Agent{model: "gpt-5.4", reasoningEffort: "high", activeIdx: -1, codexHome: codexHome}

	if got := a.GetModel(); got != "gpt-5.5" {
		t.Fatalf("GetModel() = %q, want gpt-5.5 from cc-switch current provider", got)
	}
	if got := a.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("GetReasoningEffort() = %q, want xhigh from Codex config fallback", got)
	}
}

func TestSetModel_OverridesCCSwitchCurrentForRuntime(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, ".codex")
	writeTestCodexConfig(t, codexHome, "gpt-5.4", "xhigh")
	dbPath := writeTestCCSwitchDB(t, tmp)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CC_SWITCH_DB_PATH", dbPath)

	a := &Agent{model: "gpt-5.4", activeIdx: -1, codexHome: codexHome}
	a.SetModel("gpt-5.4")

	if got := a.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want runtime override gpt-5.4", got)
	}
}

func TestAvailableModels_IncludesCCSwitchAndCodexConfigModels(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, ".codex")
	writeTestCodexConfig(t, codexHome, "gpt-5.4", "xhigh")
	dbPath := writeTestCCSwitchDB(t, tmp)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CC_SWITCH_DB_PATH", dbPath)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")

	a := &Agent{model: "gpt-5.4", activeIdx: -1, codexHome: codexHome}
	models := a.AvailableModels(context.Background())

	if len(models) < 2 {
		t.Fatalf("models = %v, want at least cc-switch current + config model", models)
	}
	if models[0].Name != "gpt-5.5" {
		t.Fatalf("first model = %q, want current cc-switch model gpt-5.5; models=%v", models[0].Name, models)
	}
	if !hasModelOption(models, "gpt-5.4") {
		t.Fatalf("models = %v, want gpt-5.4 from Codex config/other provider", models)
	}
}

func TestParseCodexRuntimeConfigMap_UsesProviderSectionKeyWhenProviderNameDiffers(t *testing.T) {
	cfg := parseCodexRuntimeConfigString(`model_provider = "cch"
model = "gpt-5.5"

[model_providers.codex_local_access]
name = "cch"
base_url = "https://cch.example.test/v1"
wire_api = "responses"
`)

	if cfg.ModelProvider != "codex_local_access" {
		t.Fatalf("ModelProvider = %q, want section key codex_local_access", cfg.ModelProvider)
	}
	if cfg.BaseURL != "https://cch.example.test/v1" {
		t.Fatalf("BaseURL = %q, want provider base_url", cfg.BaseURL)
	}
	if cfg.WireAPI != "responses" {
		t.Fatalf("WireAPI = %q, want responses", cfg.WireAPI)
	}
}

func writeTestCodexConfig(t *testing.T, codexHome, model, effort string) {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	content := `model_provider = "cch"
model = "` + model + `"
model_reasoning_effort = "` + effort + `"

[model_providers.cch]
name = "cch"
base_url = "https://cch.example.test/v1"
wire_api = "responses"

[notice.model_migrations]
"gpt-5.2" = "gpt-5.4"
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write codex config: %v", err)
	}
}

func writeTestCCSwitchDB(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, "cc-switch.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE providers (
		id TEXT,
		app_type TEXT,
		name TEXT,
		settings_config TEXT,
		is_current INTEGER
	)`)
	if err != nil {
		t.Fatalf("create providers: %v", err)
	}
	insert := func(id, name, model string, isCurrent int) {
		settings := codexSettingsJSON(t, model)
		if _, err := db.Exec(`INSERT INTO providers (id, app_type, name, settings_config, is_current) VALUES (?, 'codex', ?, ?, ?)`, id, name, settings, isCurrent); err != nil {
			t.Fatalf("insert provider %s: %v", name, err)
		}
	}
	insert("current", "ep", "gpt-5.5", 1)
	insert("other", "other", "gpt-5.4", 0)
	return dbPath
}

func codexSettingsJSON(t *testing.T, model string) string {
	t.Helper()
	cfg := `model_provider = "cch"
model = "` + model + `"

[model_providers.cch]
name = "cch"
base_url = "https://cch.example.test/v1"
wire_api = "responses"
`
	payload := map[string]any{
		"auth":   map[string]any{"OPENAI_API_KEY": "test-key"},
		"config": cfg,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	return string(b)
}

func hasModelOption(models []core.ModelOption, name string) bool {
	for _, m := range models {
		if m.Name == name {
			return true
		}
	}
	return false
}
