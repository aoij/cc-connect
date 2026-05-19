package codex

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
)

type codexSessionMeta struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

type codexStateThread struct {
	ID               string
	RolloutPath      string
	Cwd              string
	Title            string
	FirstUserMessage string
	Preview          string
	GitBranch        string
	UpdatedAt        int64
	UpdatedAtMS      int64
}

type codexSessionIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
}

// resolveCodexHomeDir returns the effective CODEX_HOME directory.
// Priority: explicit config value > CODEX_HOME env > ~/.codex
func resolveCodexHomeDir(explicit string) string {
	if h := strings.TrimSpace(explicit); h != "" {
		return h
	}
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".codex")
}

// listCodexSessions scans the codex sessions directory for JSONL transcript
// files whose cwd matches workDir.
func listCodexSessions(workDir, codexHome string) ([]core.AgentSessionInfo, error) {
	if sessions, ok, err := listCodexAppThreads(workDir, codexHome); ok {
		return sessions, err
	}
	// Fallback for older Codex installs that do not have state_*.sqlite yet.
	return listCodexSessionsFromFiles(workDir, codexHome)
}

func listCodexSessionsFromFiles(workDir, codexHome string) ([]core.AgentSessionInfo, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")

	var files []string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		return nil, nil
	}

	var sessions []core.AgentSessionInfo
	for _, f := range files {
		info := parseCodexSessionFile(f, absWorkDir)
		if info != nil {
			patchSessionSource(info.ID, codexHome)
			sessions = append(sessions, *info)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func listCodexAppThreads(workDir, codexHome string) ([]core.AgentSessionInfo, bool, error) {
	dbPath := findCodexStateDB(codexHome)
	if dbPath == "" {
		return nil, false, nil
	}

	db, err := openCodexStateDB(dbPath, true)
	if err != nil {
		return nil, true, fmt.Errorf("open codex state db: %w", err)
	}
	defer db.Close()
	indexNames := loadCodexSessionIndexNames(codexHome)
	rows, err := db.Query(`
select id, rollout_path, cwd, title, first_user_message, preview, coalesce(git_branch, ''),
       updated_at, coalesce(updated_at_ms, 0)
from threads
where archived = 0
order by coalesce(updated_at_ms, updated_at * 1000) desc, updated_at desc
`)
	if err != nil {
		return nil, true, fmt.Errorf("query codex threads: %w", err)
	}
	defer rows.Close()

	var sessions []core.AgentSessionInfo
	for rows.Next() {
		var row codexStateThread
		if err := rows.Scan(
			&row.ID,
			&row.RolloutPath,
			&row.Cwd,
			&row.Title,
			&row.FirstUserMessage,
			&row.Preview,
			&row.GitBranch,
			&row.UpdatedAt,
			&row.UpdatedAtMS,
		); err != nil {
			return nil, true, err
		}
		if !sameCodexWorkDir(row.Cwd, workDir) {
			continue
		}
		if !fileExists(row.RolloutPath) {
			continue
		}
		if name := strings.TrimSpace(indexNames[row.ID]); name != "" {
			row.Title = name
		}
		sessions = append(sessions, codexThreadToSessionInfo(row))
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return sessions, true, nil
}

func loadCodexSessionIndexNames(codexHome string) map[string]string {
	home := resolveCodexHomeDir(codexHome)
	if home == "" {
		return nil
	}
	path := filepath.Join(home, "session_index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	names := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexSessionIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		id := strings.TrimSpace(entry.ID)
		name := strings.TrimSpace(entry.ThreadName)
		if id != "" && name != "" {
			names[id] = name
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

func codexThreadToSessionInfo(row codexStateThread) core.AgentSessionInfo {
	summary := strings.TrimSpace(row.Title)
	if summary == "" {
		summary = strings.TrimSpace(row.FirstUserMessage)
	}
	if summary == "" {
		summary = strings.TrimSpace(row.Preview)
	}
	summary = strings.Join(strings.Fields(strings.ReplaceAll(summary, "\n", " ")), " ")
	summary = truncateCodexSummary(summary, 180)

	modifiedAt := time.Time{}
	if row.UpdatedAtMS > 0 {
		modifiedAt = time.UnixMilli(row.UpdatedAtMS)
	} else if row.UpdatedAt > 0 {
		modifiedAt = time.Unix(row.UpdatedAt, 0)
	}

	rolloutPath := cleanCodexPath(row.RolloutPath)
	return core.AgentSessionInfo{
		ID:           strings.TrimSpace(row.ID),
		Summary:      summary,
		MessageCount: countCodexTranscriptMessages(rolloutPath),
		ModifiedAt:   modifiedAt,
		GitBranch:    strings.TrimSpace(row.GitBranch),
	}
}

func countCodexTranscriptMessages(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Type != "response_item" {
			continue
		}
		var item struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(entry.Payload, &item) != nil {
			continue
		}
		if item.Role == "assistant" {
			count++
			continue
		}
		if item.Role == "user" {
			for _, c := range item.Content {
				if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
					count++
					break
				}
			}
		}
	}
	return count
}

func truncateCodexSummary(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

// parseCodexSessionFile reads a Codex JSONL transcript.
// Returns nil if the session's cwd doesn't match filterCwd.
func parseCodexSessionFile(path, filterCwd string) *core.AgentSessionInfo {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil
	}

	var sessionID string
	var sessionCwd string
	var summary string
	var msgCount int
	userMsgSeen := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		switch entry.Type {
		case "session_meta":
			var meta codexSessionMeta
			if json.Unmarshal(entry.Payload, &meta) == nil {
				sessionID = meta.ID
				sessionCwd = meta.Cwd
			}

		case "response_item":
			var item struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(entry.Payload, &item) == nil {
				if item.Role == "user" {
					userMsgSeen++
					msgCount++
					// The actual user prompt is the last user response_item
					// (earlier ones are system/AGENTS.md instructions).
					// Pick the last content block that looks like a real prompt.
					for _, c := range item.Content {
						if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
							summary = c.Text
						}
					}
				} else if item.Role == "assistant" {
					msgCount++
				}
			}
		}
	}

	// Filter by cwd
	if filterCwd != "" && sessionCwd != "" && sessionCwd != filterCwd {
		return nil
	}

	if sessionID == "" {
		return nil
	}

	if len([]rune(summary)) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}

	return &core.AgentSessionInfo{
		ID:           sessionID,
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   stat.ModTime(),
	}
}

func scanSessionMetaIDs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	seen := map[string]struct{}{}
	var ids []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Type != "session_meta" {
			continue
		}
		var meta codexSessionMeta
		if err := json.Unmarshal(entry.Payload, &meta); err != nil {
			continue
		}
		id := strings.TrimSpace(meta.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func findCodexStateDB(codexHome string) string {
	home := resolveCodexHomeDir(codexHome)
	if home == "" {
		return ""
	}

	var candidates []string
	if p := filepath.Join(home, "state_5.sqlite"); fileExists(p) {
		candidates = append(candidates, p)
	}
	if matches, _ := filepath.Glob(filepath.Join(home, "state_*.sqlite")); len(matches) > 0 {
		candidates = append(candidates, matches...)
	}
	if p := filepath.Join(home, "state.sqlite"); fileExists(p) {
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return ""
	}

	seen := make(map[string]struct{}, len(candidates))
	type candidate struct {
		path string
		mod  time.Time
	}
	var unique []candidate
	for _, p := range candidates {
		clean := cleanCodexPath(p)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			continue
		}
		unique = append(unique, candidate{path: clean, mod: info.ModTime()})
	}
	if len(unique) == 0 {
		return ""
	}
	sort.Slice(unique, func(i, j int) bool {
		return unique[i].mod.After(unique[j].mod)
	})
	return unique[0].path
}

func openCodexStateDB(path string, readOnly bool) (*sql.DB, error) {
	path = filepath.ToSlash(cleanCodexPath(path))
	mode := "rwc"
	if readOnly {
		mode = "ro"
	}
	dsn := fmt.Sprintf("file:%s?mode=%s&_pragma=busy_timeout(5000)", path, mode)
	return sql.Open("sqlite", dsn)
}

func cleanCodexPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	path = strings.TrimPrefix(path, `\??\`)
	return filepath.Clean(path)
}

func normalizedCodexPath(path string) string {
	path = cleanCodexPath(path)
	if path == "" || path == "." {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		path = eval
	}
	path = filepath.Clean(path)
	return strings.ToLower(path)
}

func sameCodexWorkDir(a, b string) bool {
	na := normalizedCodexPath(a)
	nb := normalizedCodexPath(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(cleanCodexPath(path))
	return err == nil && !info.IsDir()
}

func findCodexThreadRolloutPath(sessionID, codexHome string) string {
	dbPath := findCodexStateDB(codexHome)
	if dbPath == "" {
		return ""
	}
	db, err := openCodexStateDB(dbPath, true)
	if err != nil {
		return ""
	}
	defer db.Close()

	var path string
	if err := db.QueryRow(`select rollout_path from threads where id = ?`, sessionID).Scan(&path); err != nil {
		return ""
	}
	path = cleanCodexPath(path)
	if fileExists(path) {
		return path
	}
	return ""
}

// findSessionFile locates the JSONL transcript for a given session ID.
func findSessionFile(sessionID, codexHome string) string {
	if path := findCodexThreadRolloutPath(sessionID, codexHome); path != "" {
		return path
	}

	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")

	var found string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.Contains(filepath.Base(path), sessionID) {
			found = path
			return nil
		}
		ids, scanErr := scanSessionMetaIDs(path)
		if scanErr != nil {
			return nil
		}
		for _, id := range ids {
			if id == sessionID {
				found = path
				break
			}
		}
		if found != "" {
			found = path
		}
		return nil
	})
	return found
}

// getSessionHistory reads the JSONL transcript and returns user/assistant messages.
func getSessionHistory(sessionID, codexHome string, limit int) ([]core.HistoryEntry, error) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return nil, fmt.Errorf("session file not found for %s", sessionID)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []core.HistoryEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if raw.Type != "response_item" {
			continue
		}

		var item struct {
			Role    string `json:"role"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw.Payload, &item) != nil {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)

		switch {
		case item.Role == "user" && len(item.Content) > 0:
			for _, c := range item.Content {
				if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
					entries = append(entries, core.HistoryEntry{
						Role: "user", Content: c.Text, Timestamp: ts,
					})
				}
			}
		case item.Role == "assistant" && len(item.Content) > 0:
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					entries = append(entries, core.HistoryEntry{
						Role: "assistant", Content: c.Text, Timestamp: ts,
					})
				}
			}
		case item.Type == "reasoning" && item.Text != "":
			// skip reasoning items
		}
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func deleteCodexSession(sessionID, codexHome string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("empty session id")
	}

	archived, err := archiveCodexThread(sessionID, codexHome)
	if err != nil {
		return err
	}
	if archived {
		return nil
	}

	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return fmt.Errorf("session file not found: %s", sessionID)
	}
	return os.Remove(path)
}

func archiveCodexThread(sessionID, codexHome string) (bool, error) {
	dbPath := findCodexStateDB(codexHome)
	if dbPath == "" {
		return false, nil
	}

	db, err := openCodexStateDB(dbPath, false)
	if err != nil {
		return false, fmt.Errorf("open codex state db: %w", err)
	}
	defer db.Close()

	now := time.Now()
	nowSec := now.Unix()
	nowMS := now.UnixMilli()
	res, err := db.Exec(`
update threads
set archived = 1,
    archived_at = ?,
    updated_at = ?,
    updated_at_ms = ?
where id = ?
`, nowSec, nowSec, nowMS, sessionID)
	if err != nil {
		return false, fmt.Errorf("archive codex thread: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return false, nil
	}

	if path := findSessionFile(sessionID, codexHome); path != "" {
		if archivedPath, err := moveCodexRolloutToArchive(path, codexHome); err == nil && archivedPath != "" {
			_, _ = db.Exec(`update threads set rollout_path = ? where id = ?`, archivedPath, sessionID)
		}
	}
	return true, nil
}

func moveCodexRolloutToArchive(path, codexHome string) (string, error) {
	path = cleanCodexPath(path)
	if !fileExists(path) {
		return "", nil
	}

	home := resolveCodexHomeDir(codexHome)
	if home == "" {
		return "", fmt.Errorf("codex home not found")
	}
	archiveDir := filepath.Join(home, "archived_sessions")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", err
	}
	if sameCodexWorkDir(filepath.Dir(path), archiveDir) {
		return path, nil
	}

	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	dest := filepath.Join(archiveDir, filepath.Base(path))
	if fileExists(dest) {
		dest = filepath.Join(archiveDir, fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
	}
	if err := os.Rename(path, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// patchSessionSource rewrites the session_meta line in a Codex JSONL transcript
// so that source="cli" and originator="codex_cli_rs", making the session visible
// in the interactive `codex` terminal.
func patchSessionSource(sessionID, codexHome string) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		return
	}
	firstLine := data[:idx]

	// Only patch if it's actually an exec-sourced session
	if !bytes.Contains(firstLine, []byte(`"source":"exec"`)) {
		return
	}

	patched := bytes.Replace(firstLine, []byte(`"source":"exec"`), []byte(`"source":"cli"`), 1)
	patched = bytes.Replace(patched, []byte(`"originator":"codex_exec"`), []byte(`"originator":"codex_cli_rs"`), 1)

	if bytes.Equal(patched, firstLine) {
		return
	}

	out := make([]byte, 0, len(patched)+len(data)-idx)
	out = append(out, patched...)
	out = append(out, data[idx:]...)

	_ = os.WriteFile(path, out, 0o644)
}

// isUserPrompt returns true if the text looks like an actual user prompt
// rather than system context (AGENTS.md, environment_context, permissions, etc.)
func isUserPrompt(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// Skip XML-style system context
	if strings.HasPrefix(t, "<") {
		return false
	}
	// Skip AGENTS.md instructions injected by Codex
	if strings.HasPrefix(t, "# AGENTS.md") || strings.HasPrefix(t, "#AGENTS.md") {
		return false
	}
	return true
}
