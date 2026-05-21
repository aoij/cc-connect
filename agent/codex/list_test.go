package codex

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindSessionFile_MatchesSessionMetaIDEvenWhenFilenameDiffers(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "05", "18")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	targetID := "019dcde6-f301-75a2-b242-9d08dffac0fb"
	filePath := filepath.Join(sessionsDir, "rollout-2026-05-18T10-00-00-019dd299-8fa6-7c40-87f6-324b7daae9ab.jsonl")
	content := `{"timestamp":"2026-05-18T02:00:00Z","type":"session_meta","payload":{"id":"019dd299-8fa6-7c40-87f6-324b7daae9ab","cwd":"C:\\ai_work"}}` + "\n" +
		`{"timestamp":"2026-05-18T02:00:01Z","type":"session_meta","payload":{"id":"` + targetID + `","cwd":"C:\\ai_work"}}` + "\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findSessionFile(targetID, codexHome)
	if got != filePath {
		t.Fatalf("findSessionFile() = %q, want %q", got, filePath)
	}
}

func TestScanSessionMetaIDs_DeduplicatesIDs(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sample.jsonl")
	content := `{"type":"session_meta","payload":{"id":"a"}}` + "\n" +
		`{"type":"session_meta","payload":{"id":"a"}}` + "\n" +
		`{"type":"session_meta","payload":{"id":"b"}}` + "\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := scanSessionMetaIDs(filePath)
	if err != nil {
		t.Fatalf("scanSessionMetaIDs() error = %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("scanSessionMetaIDs() = %#v, want [a b]", ids)
	}
}

func TestListCodexSessions_UsesCodexAppStateThreads(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	otherDir := filepath.Join(tmpDir, "other")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}

	activeRollout := writeTestRollout(t, codexHome, "session-active", workDir)
	archivedRollout := writeTestRollout(t, codexHome, "session-archived", workDir)
	otherRollout := writeTestRollout(t, codexHome, "session-other", otherDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-active", activeRollout, workDir, "Codex App 标题", 0, 2000, "feature/test")
	insertTestThread(t, db, "session-archived", archivedRollout, workDir, "归档标题", 1, 3000, "")
	insertTestThread(t, db, "session-other", otherRollout, otherDir, "其他目录", 0, 4000, "")

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listCodexSessions() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].ID != "session-active" || got[0].Summary != "Codex App 标题" {
		t.Fatalf("listCodexSessions()[0] = %#v", got[0])
	}
	if got[0].MessageCount != 2 {
		t.Fatalf("MessageCount = %d, want 2", got[0].MessageCount)
	}
	if got[0].GitBranch != "feature/test" {
		t.Fatalf("GitBranch = %q, want feature/test", got[0].GitBranch)
	}
}

func TestListCodexSessions_DoesNotFilterByHeartbeatPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	visibleRollout := writeTestRollout(t, codexHome, "session-visible", workDir)
	hiddenRollout := writeTestRollout(t, codexHome, "session-hidden", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-visible", visibleRollout, workDir, "Visible", 0, 2000, "")
	insertTestThread(t, db, "session-hidden", hiddenRollout, workDir, "Hidden", 0, 3000, "")
	writeTestSessionIndex(t, codexHome, codexSessionIndexEntry{ID: "session-visible", ThreadName: "Visible"}, codexSessionIndexEntry{ID: "session-hidden", ThreadName: "Hidden"})
	writeTestGlobalState(t, codexHome, "session-visible")

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listCodexSessions() len = %d, want 2: %#v", len(got), got)
	}
	if got[0].ID != "session-hidden" || got[1].ID != "session-visible" {
		t.Fatalf("listCodexSessions() = %#v, want all state DB threads ordered by updated time", got)
	}
}

func TestListCodexSessions_UsesSessionIndexThreadName(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rollout := writeTestRollout(t, codexHome, "session-indexed", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-indexed", rollout, workDir, "long original first prompt title", 0, 2000, "")
	writeTestSessionIndex(t, codexHome, codexSessionIndexEntry{ID: "session-indexed", ThreadName: "Index title"})

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listCodexSessions() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Summary != "Index title" {
		t.Fatalf("Summary = %q, want session_index thread_name", got[0].Summary)
	}
}

func TestListCodexSessions_SessionIndexOverridesThreadsTitle(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rollout := writeTestRollout(t, codexHome, "session-title", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-title", rollout, workDir, "Desktop title", 0, 2000, "")
	writeTestSessionIndex(t, codexHome, codexSessionIndexEntry{ID: "session-title", ThreadName: "Index title"})

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listCodexSessions() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Summary != "Index title" {
		t.Fatalf("Summary = %q, want session_index thread_name", got[0].Summary)
	}
}

func TestListCodexSessions_FollowsDesktopVisibilityFilters(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userRollout := writeTestRollout(t, codexHome, "session-user", workDir)
	noUserEventRollout := writeTestRollout(t, codexHome, "session-no-user-event", workDir)
	emptyPreviewRollout := writeTestRollout(t, codexHome, "session-empty-preview", workDir)
	execRollout := writeTestRollout(t, codexHome, "session-exec", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-user", userRollout, workDir, "User session", 0, 1000, "")
	insertTestThread(t, db, "session-no-user-event", noUserEventRollout, workDir, "No user event", 0, 2000, "")
	insertTestThread(t, db, "session-empty-preview", emptyPreviewRollout, workDir, "Empty preview", 0, 3000, "")
	insertTestThread(t, db, "session-exec", execRollout, workDir, "Exec source", 0, 4000, "")
	if _, err := db.Exec(`update threads set has_user_event = 0 where id = ?`, "session-no-user-event"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update threads set preview = '' where id = ?`, "session-empty-preview"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update threads set source = 'exec' where id = ?`, "session-exec"); err != nil {
		t.Fatal(err)
	}
	writeTestSessionIndex(t, codexHome,
		codexSessionIndexEntry{ID: "session-no-user-event", ThreadName: "Indexed visible"},
		codexSessionIndexEntry{ID: "session-empty-preview", ThreadName: "Indexed but empty preview"},
	)

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listCodexSessions() len = %d, want 2: %#v", len(got), got)
	}
	if got[0].ID != "session-no-user-event" || got[0].Summary != "Indexed visible" || got[1].ID != "session-user" {
		t.Fatalf("listCodexSessions() = %#v, want no-user-event interactive + user session", got)
	}
}

func TestListCodexSessions_FiltersByCodexConfigModelProvider(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(`model_provider = "codex_local_access"`), 0o644); err != nil {
		t.Fatal(err)
	}

	keepRollout := writeTestRollout(t, codexHome, "session-keep", workDir)
	otherRollout := writeTestRollout(t, codexHome, "session-other-provider", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-keep", keepRollout, workDir, "Keep", 0, 1000, "")
	insertTestThread(t, db, "session-other-provider", otherRollout, workDir, "Other provider", 0, 2000, "")
	if _, err := db.Exec(`update threads set model_provider = 'cch' where id = ?`, "session-other-provider"); err != nil {
		t.Fatal(err)
	}

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "session-keep" {
		t.Fatalf("listCodexSessions() = %#v, want only codex_local_access provider", got)
	}
}

func TestListCodexSessions_IncludesThreadWhenRolloutMissing(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	missingRollout := filepath.Join(codexHome, "sessions", "missing.jsonl")
	insertTestThread(t, db, "session-missing-rollout", missingRollout, workDir, "Visible from DB", 0, 2000, "")

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listCodexSessions() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].ID != "session-missing-rollout" || got[0].Summary != "Visible from DB" {
		t.Fatalf("listCodexSessions()[0] = %#v", got[0])
	}
	if got[0].MessageCount != 0 {
		t.Fatalf("MessageCount = %d, want 0 for missing rollout", got[0].MessageCount)
	}
}

func TestFindCodexStateDB_PrefersState5(t *testing.T) {
	codexHome := t.TempDir()
	old := filepath.Join(codexHome, "state_4.sqlite")
	state5 := filepath.Join(codexHome, "state_5.sqlite")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state5, []byte("state5"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(old, future, future); err != nil {
		t.Fatal(err)
	}

	got := findCodexStateDB(codexHome)
	if got != filepath.Clean(state5) {
		t.Fatalf("findCodexStateDB() = %q, want state_5.sqlite %q", got, state5)
	}
}

func TestListCodexSessions_TruncatesLongCodexAppTitles(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rollout := writeTestRollout(t, codexHome, "session-long", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	longTitle := ""
	for i := 0; i < 240; i++ {
		longTitle += "长"
	}
	insertTestThread(t, db, "session-long", rollout, workDir, longTitle, 0, 2000, "")

	got, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listCodexSessions() len = %d, want 1", len(got))
	}
	if len([]rune(got[0].Summary)) != 180 {
		t.Fatalf("summary rune len = %d, want 180", len([]rune(got[0].Summary)))
	}
	if got[0].Summary[len(got[0].Summary)-3:] != "..." {
		t.Fatalf("summary = %q, want ellipsis suffix", got[0].Summary)
	}
}

func TestDeleteCodexSession_ArchivesCodexAppThread(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	workDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rollout := writeTestRollout(t, codexHome, "session-delete", workDir)
	db := createTestCodexStateDB(t, codexHome)
	defer db.Close()
	insertTestThread(t, db, "session-delete", rollout, workDir, "Delete me", 0, time.Now().Unix(), "")

	if err := deleteCodexSession("session-delete", codexHome); err != nil {
		t.Fatalf("deleteCodexSession() error = %v", err)
	}

	var archived int
	var archivedPath string
	if err := db.QueryRow(`select archived, rollout_path from threads where id = ?`, "session-delete").Scan(&archived, &archivedPath); err != nil {
		t.Fatal(err)
	}
	if archived != 1 {
		t.Fatalf("archived = %d, want 1", archived)
	}
	if fileExists(rollout) {
		t.Fatalf("original rollout still exists: %s", rollout)
	}
	if !fileExists(archivedPath) {
		t.Fatalf("archived rollout does not exist: %s", archivedPath)
	}

	sessions, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("listCodexSessions() after delete = %#v, want empty", sessions)
	}
}

func createTestCodexStateDB(t *testing.T, codexHome string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(codexHome, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
create table threads (
	id text primary key,
	rollout_path text,
	cwd text,
	title text,
	first_user_message text,
	preview text,
	git_branch text,
	archived integer,
	updated_at integer,
	updated_at_ms integer,
	archived_at integer,
	has_user_event integer,
	source text,
	thread_source text,
	model_provider text
)
`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func writeTestGlobalState(t *testing.T, codexHome string, ids ...string) {
	t.Helper()
	entries := ""
	for i, id := range ids {
		if i > 0 {
			entries += ","
		}
		entries += `"` + id + `":{"approvalPolicy":"never"}`
	}
	content := `{"electron-persisted-atom-state":{"heartbeat-thread-permissions-by-id":{` + entries + `}}}`
	if err := os.WriteFile(filepath.Join(codexHome, ".codex-global-state.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestSessionIndex(t *testing.T, codexHome string, entries ...codexSessionIndexEntry) {
	t.Helper()
	var content string
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		content += string(data) + "\n"
	}
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func insertTestThread(t *testing.T, db *sql.DB, id, rolloutPath, cwd, title string, archived int, updatedAt int64, gitBranch string) {
	t.Helper()
	_, err := db.Exec(`
insert into threads (id, rollout_path, cwd, title, first_user_message, preview, git_branch, archived, updated_at, updated_at_ms, has_user_event, source, thread_source, model_provider)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, rolloutPath, cwd, title, "first message", "preview message", gitBranch, archived, updatedAt, updatedAt*1000, 1, "vscode", "user", "codex_local_access")
	if err != nil {
		t.Fatal(err)
	}
}

func writeTestRollout(t *testing.T, codexHome, sessionID, cwd string) string {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "05", "18")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-05-18T10-00-00-"+sessionID+".jsonl")
	content := `{"timestamp":"2026-05-18T02:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + filepath.ToSlash(cwd) + `"}}` + "\n" +
		`{"timestamp":"2026-05-18T02:00:01Z","type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"hello"}]}}` + "\n" +
		`{"timestamp":"2026-05-18T02:00:02Z","type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
