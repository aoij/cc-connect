package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("codex", New)
}

// Agent drives OpenAI Codex CLI using `codex exec --json`.
//
// Modes (maps to codex exec flags):
//   - "suggest":   default, no special flags (safe commands only)
//   - "auto-edit": --full-auto (sandbox-protected auto execution)
//   - "full-auto": --full-auto (sandbox-protected auto execution)
//   - "yolo":      --dangerously-bypass-approvals-and-sandbox
type Agent struct {
	workDir          string
	model            string
	reasoningEffort  string
	mode             string // "suggest" | "auto-edit" | "full-auto" | "yolo"
	backend          string // "exec" | "app_server"
	appServerURL     string
	codexHome        string
	cliBin           string   // CLI binary name, default "codex"
	cliExtraArgs     []string // extra args parsed from cli_path after the binary
	providers        []core.ProviderConfig
	activeIdx        int // -1 = no provider set
	sessionEnv       []string
	modelOverridden  bool
	effortOverridden bool
	mu               sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	reasoningEffort, _ := opts["reasoning_effort"].(string)
	mode, _ := opts["mode"].(string)
	backend, _ := opts["backend"].(string)
	appServerURL, _ := opts["app_server_url"].(string)
	codexHome, _ := opts["codex_home"].(string)
	mode = normalizeMode(mode)
	backend = normalizeBackend(backend)

	if appServerURL == "" {
		appServerURL = "ws://127.0.0.1:3845"
	} else if strings.EqualFold(strings.TrimSpace(appServerURL), "stdio") {
		appServerURL = ""
	}

	// cli_path allows overriding the binary, e.g. "omx" or "omx --flag val"
	cliBin := "codex"
	var cliExtraArgs []string
	if cliPath, _ := opts["cli_path"].(string); strings.TrimSpace(cliPath) != "" {
		parts := strings.Fields(cliPath)
		cliBin = parts[0]
		if len(parts) > 1 {
			cliExtraArgs = parts[1:]
		}
	}

	if _, err := exec.LookPath(cliBin); err != nil {
		return nil, fmt.Errorf("codex: %q CLI not found in PATH, install with: npm install -g @openai/codex", cliBin)
	}

	return &Agent{
		workDir:         workDir,
		model:           model,
		reasoningEffort: normalizeReasoningEffort(reasoningEffort),
		mode:            mode,
		backend:         backend,
		appServerURL:    appServerURL,
		codexHome:       strings.TrimSpace(codexHome),
		cliBin:          cliBin,
		cliExtraArgs:    cliExtraArgs,
		activeIdx:       -1,
	}, nil
}

func normalizeBackend(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "app-server", "app_server", "appserver", "ws":
		return "app_server"
	default:
		return "exec"
	}
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto-edit", "autoedit", "auto_edit", "edit":
		return "auto-edit"
	case "full-auto", "fullauto", "full_auto", "auto":
		return "full-auto"
	case "yolo", "bypass", "dangerously-bypass":
		return "yolo"
	default:
		return "suggest"
	}
}

func normalizeReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "low":
		return "low"
	case "medium", "med":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "x-high", "very-high":
		return "xhigh"
	default:
		return ""
	}
}

func (a *Agent) Name() string { return "codex" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("codex: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	a.modelOverridden = true
	slog.Info("codex: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	model, _ := a.effectiveModelAndReasoning()
	return model
}

func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningEffort = normalizeReasoningEffort(effort)
	a.effortOverridden = true
	slog.Info("codex: reasoning effort changed", "reasoning_effort", a.reasoningEffort)
}

func (a *Agent) GetReasoningEffort() string {
	_, effort := a.effectiveModelAndReasoning()
	return effort
}

func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"low", "medium", "high", "xhigh"}
}

func (a *Agent) configuredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	a.mu.RLock()
	codexHome := a.codexHome
	a.mu.RUnlock()

	var models []core.ModelOption
	models = appendUniqueModelOptions(models, readCodexConfigModelOptions(codexHome)...)
	models = appendUniqueModelOptions(models, a.fetchModelsFromAPI(ctx)...)
	models = appendUniqueModelOptions(models, readCodexCachedModels(codexHome)...)
	models = appendUniqueModelOptions(models, a.configuredModels()...)
	models = appendUniqueModelOptions(models, defaultCodexModelOptions()...)
	return prioritizeModelOptions(a.GetModel(), models)
}

func (a *Agent) effectiveModelAndReasoning() (model string, effort string) {
	a.mu.RLock()
	providers := append([]core.ProviderConfig(nil), a.providers...)
	activeIdx := a.activeIdx
	model = strings.TrimSpace(a.model)
	effort = strings.TrimSpace(a.reasoningEffort)
	codexHome := a.codexHome
	modelOverridden := a.modelOverridden
	effortOverridden := a.effortOverridden
	a.mu.RUnlock()

	if providerModel := strings.TrimSpace(core.GetProviderModel(providers, activeIdx, "")); providerModel != "" {
		model = providerModel
	}
	if activeIdx >= 0 && activeIdx < len(providers) {
		return model, effort
	}

	defaults := readCodexRuntimeDefaults(codexHome)
	if !modelOverridden && strings.TrimSpace(defaults.Model) != "" {
		model = strings.TrimSpace(defaults.Model)
	}
	if !effortOverridden && strings.TrimSpace(defaults.ReasoningEffort) != "" {
		effort = normalizeReasoningEffort(defaults.ReasoningEffort)
	}
	return model, effort
}

func defaultCodexModelOptions() []core.ModelOption {
	return []core.ModelOption{
		{Name: "gpt-5.5", Desc: "GPT-5.5 (frontier)"},
		{Name: "gpt-5.4", Desc: "GPT-5.4 (balanced)"},
		{Name: "gpt-5.4-mini", Desc: "GPT-5.4 Mini (fast)"},
		{Name: "gpt-5.3-codex", Desc: "GPT-5.3 Codex (code-optimized)"},
		{Name: "gpt-5.2", Desc: "GPT-5.2"},
		{Name: "o4-mini", Desc: "O4 Mini (fast reasoning)"},
		{Name: "o3", Desc: "O3 (reasoning)"},
		{Name: "codex-mini-latest", Desc: "Codex Mini (code-optimized)"},
	}
}

func (a *Agent) fetchModelsFromAPI(ctx context.Context) []core.ModelOption {
	a.mu.Lock()
	apiKey := ""
	baseURL := ""
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		apiKey = a.providers[a.activeIdx].APIKey
		baseURL = a.providers[a.activeIdx].BaseURL
	}
	a.mu.Unlock()

	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = os.Getenv("OPENAI_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("codex: failed to fetch models", "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var models []core.ModelOption
	for _, m := range result.Data {
		if isLikelyCodexChatModel(m.ID) {
			models = append(models, core.ModelOption{Name: m.ID})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

func isLikelyCodexChatModel(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false
	}
	blocked := []string{"embedding", "moderation", "whisper", "tts", "audio", "transcribe", "image", "vision", "realtime"}
	for _, token := range blocked {
		if strings.Contains(id, token) {
			return false
		}
	}
	return strings.HasPrefix(id, "gpt-") ||
		strings.HasPrefix(id, "o1") ||
		strings.HasPrefix(id, "o3") ||
		strings.HasPrefix(id, "o4") ||
		strings.Contains(id, "codex")
}

func readCodexCachedModels(codexHomeArg ...string) []core.ModelOption {
	codexHome := ""
	if len(codexHomeArg) > 0 {
		codexHome = strings.TrimSpace(codexHomeArg[0])
	}
	codexHome = resolveCodexHomeDir(codexHome)
	if codexHome == "" {
		return nil
	}
	path := filepath.Join(codexHome, "models_cache.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var payload struct {
		Models []struct {
			Slug           string `json:"slug"`
			DisplayName    string `json:"display_name"`
			Description    string `json:"description"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil
	}

	var models []core.ModelOption
	seen := make(map[string]struct{}, len(payload.Models))
	for _, m := range payload.Models {
		name := strings.TrimSpace(m.Slug)
		if name == "" {
			name = strings.TrimSpace(m.DisplayName)
		}
		if name == "" {
			continue
		}
		if m.Visibility != "" && m.Visibility != "list" {
			continue
		}
		if !m.SupportedInAPI {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, core.ModelOption{
			Name: name,
			Desc: strings.TrimSpace(m.Description),
		})
	}
	return models
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	workDir := a.workDir
	mode := a.mode
	model := a.model
	reasoningEffort := a.reasoningEffort
	backend := a.backend
	appServerURL := a.appServerURL
	codexHome := a.codexHome
	cliBin := a.cliBin
	cliExtraArgs := a.cliExtraArgs
	modelOverridden := a.modelOverridden
	effortOverridden := a.effortOverridden
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	var baseURL string
	hasActiveProvider := a.activeIdx >= 0 && a.activeIdx < len(a.providers)
	if hasActiveProvider {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
		baseURL = a.providers[a.activeIdx].BaseURL
	}
	provName, provAPIKey, provWireAPI, provHeaders := a.activeProviderCodexConfig()
	a.mu.Unlock()

	if !hasActiveProvider {
		defaults := readCodexRuntimeDefaults(codexHome)
		if !modelOverridden && strings.TrimSpace(defaults.Model) != "" {
			model = strings.TrimSpace(defaults.Model)
		}
		if !effortOverridden && strings.TrimSpace(defaults.ReasoningEffort) != "" {
			reasoningEffort = normalizeReasoningEffort(defaults.ReasoningEffort)
		}
		if strings.TrimSpace(defaults.BaseURL) != "" {
			baseURL = strings.TrimSpace(defaults.BaseURL)
			extraEnv = upsertEnv(extraEnv, "OPENAI_BASE_URL", baseURL)
		}
		if strings.TrimSpace(defaults.APIKey) != "" {
			extraEnv = upsertEnv(extraEnv, "OPENAI_API_KEY", strings.TrimSpace(defaults.APIKey))
		}
		if strings.TrimSpace(defaults.ModelProvider) != "" {
			provName = strings.TrimSpace(defaults.ModelProvider)
			provAPIKey = strings.TrimSpace(defaults.APIKey)
			provWireAPI = strings.TrimSpace(defaults.WireAPI)
			provHeaders = defaults.HTTPHeaders
		}
	}

	if provName != "" {
		if err := ensureCodexProviderConfig(codexHome, provName, baseURL, provWireAPI, provHeaders); err != nil {
			slog.Warn("codex: failed to write provider config", "provider", provName, "error", err)
		}
		if err := ensureCodexAuth(codexHome, provAPIKey); err != nil {
			slog.Warn("codex: failed to write auth.json", "provider", provName, "error", err)
		}
	}

	if backend == "app_server" {
		return newAppServerSession(ctx, appServerURL, workDir, model, reasoningEffort, mode, sessionID, baseURL, provName, extraEnv, codexHome)
	}
	if codexHome != "" {
		extraEnv = append(extraEnv, "CODEX_HOME="+codexHome)
	}
	extraEnv = upsertEnv(extraEnv, "GIT_TERMINAL_PROMPT", "0")
	extraEnv = upsertEnv(extraEnv, "GCM_INTERACTIVE", "never")

	return newCodexSession(ctx, cliBin, cliExtraArgs, workDir, model, reasoningEffort, mode, sessionID, baseURL, extraEnv, provName)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	codexHome := a.codexHome
	workDir := a.workDir
	a.mu.RUnlock()
	return listCodexSessions(workDir, codexHome)
}

func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	a.mu.RLock()
	codexHome := a.codexHome
	a.mu.RUnlock()
	return getSessionHistory(sessionID, codexHome, limit)
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	a.mu.RLock()
	codexHome := a.codexHome
	a.mu.RUnlock()
	return deleteCodexSession(sessionID, codexHome)
}

func (a *Agent) Stop() error { return nil }

// SetMode changes the approval mode for future sessions.
func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("codex: approval mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	opts := map[string]any{
		"mode":    a.mode,
		"backend": a.backend,
	}
	if a.model != "" {
		opts["model"] = a.model
	}
	if a.reasoningEffort != "" {
		opts["reasoning_effort"] = a.reasoningEffort
	}
	if a.appServerURL != "" {
		opts["app_server_url"] = a.appServerURL
	}
	if a.codexHome != "" {
		opts["codex_home"] = a.codexHome
	}
	return opts
}

// ── SkillProvider implementation ──────────────────────────────

func (a *Agent) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return codexSkillDirs(absDir, a.codexHome)
}

// ── ContextCompressor implementation ──────────────────────────

// CompressCommand returns "" because Codex native slash commands (/compact, /clear)
// are not reliably executed in exec/resume mode — they may be treated as plain text.
// See: https://github.com/chenhg5/cc-connect/issues/378
func (a *Agent) CompressCommand() string { return "" }

func codexSkillDirs(workDir, explicitCodexHome string) []string {
	homeDir, _ := os.UserHomeDir()
	codexHome := strings.TrimSpace(explicitCodexHome)
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if codexHome == "" && homeDir != "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}

	projectDirs := walkUpCodexProjectSkillDirs(workDir, homeDir)
	userDirs := make([]string, 0, 2)
	if codexHome != "" {
		userDirs = append(userDirs, filepath.Join(codexHome, "skills"))
	}
	if homeDir != "" {
		userDirs = append(userDirs, filepath.Join(homeDir, ".agents", "skills"))
	}
	return uniqueCodexSkillDirs(append(projectDirs, userDirs...))
}

func walkUpCodexProjectSkillDirs(workDir, homeDir string) []string {
	current := filepath.Clean(workDir)
	homeDir = filepath.Clean(homeDir)
	stopAt := findCodexProjectRoot(current)

	var dirs []string
	for {
		if homeDir != "" && sameCodexPath(current, homeDir) {
			break
		}
		dirs = append(dirs,
			filepath.Join(current, ".agents", "skills"),
			filepath.Join(current, ".codex", "skills"),
		)
		if stopAt != "" && sameCodexPath(current, stopAt) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return uniqueCodexSkillDirs(dirs)
}

func findCodexProjectRoot(start string) string {
	current := filepath.Clean(start)
	for {
		for _, marker := range []string{".git", ".jj"} {
			if _, err := os.Stat(filepath.Join(current, marker)); err == nil {
				return current
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func sameCodexPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func uniqueCodexSkillDirs(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

// ── MemoryFileProvider implementation ─────────────────────────

func (a *Agent) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}
	return filepath.Join(codexHome, "AGENTS.md")
}

// ── ProviderSwitcher implementation ──────────────────────────

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		slog.Info("codex: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("codex: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]core.ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string
	if p.APIKey != "" {
		env = append(env, "OPENAI_API_KEY="+p.APIKey)
	}
	if p.BaseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+p.BaseURL)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// activeProviderCodexConfig returns Codex-specific config for the active provider.
// Returns non-empty name when the provider has codex config (wire_api, headers)
// OR when it has a BaseURL (third-party provider needing auth.json).
func (a *Agent) activeProviderCodexConfig() (name string, apiKey string, wireAPI string, headers map[string]string) {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return
	}
	p := a.providers[a.activeIdx]
	hasCodexConfig := p.CodexWireAPI != "" || len(p.CodexHTTPHeaders) > 0
	isThirdParty := p.BaseURL != "" && p.APIKey != ""
	if !hasCodexConfig && !isThirdParty {
		return
	}
	return p.Name, p.APIKey, p.CodexWireAPI, p.CodexHTTPHeaders
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "suggest", Name: "Suggest", NameZh: "建议", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "auto-edit", Name: "Auto Edit", NameZh: "自动编辑", Desc: "Auto-approve file edits, ask for shell commands", DescZh: "自动允许文件编辑，Shell 命令需确认"},
		{Key: "full-auto", Name: "Full Auto", NameZh: "全自动", Desc: "Auto-approve with workspace sandbox", DescZh: "自动通过（工作区沙箱）"},
		{Key: "yolo", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Bypass all approvals and sandbox", DescZh: "跳过所有审批和沙箱"},
	}
}
