package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMCP talks to the MCP server as an AI coding tool does: one JSON-RPC message per line.
func TestMCP(t *testing.T) {
	f := &fakeAline{token: testToken, noStats: true, devType: map[string]any{"devTypeTitle": "KEEPER", "agilityStat": 50,
		"agilityReasoning": "You commit on few days.", "consistencyReasoningAi": "", "devTypeDescriptionAi": "Steady with AI.", "shareCode": "x"}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(o io.Writer) { out = o }(out)
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}))
	must(saveRepos(dir, []Repo{{ID: "a", Name: "app", Path: "/home/me/code/app", Provider: "GITHUB", Namespace: "me/app"}}))
	must(writeCommits(dir, []Commit{{Repo: "a", Hash: "1", Date: "2026-09-01T12:00:00Z", CreationType: creationHuman,
		Files: []FileStat{{Name: "main.go", Add: 3, Created: true}}}}))

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"devtype","arguments":{"refresh":true}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"stack"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"portfolio"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"sync"}}`,
		`{"jsonrpc":"2.0","id":"x","method":"ping"}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":9,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{not json`,
	}, "\n")
	var buf bytes.Buffer
	must(serveMCP(dir, strings.NewReader(in), &buf))

	type response struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			ProtocolVersion string                  `json:"protocolVersion"`
			Tools           []mcpTool               `json:"tools"`
			Content         []struct{ Text string } `json:"content"`
			IsError         bool                    `json:"isError"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	got := map[string]response{}
	var order []string
	dec := json.NewDecoder(&buf)
	for dec.More() {
		var r response
		must(dec.Decode(&r))
		got[string(r.ID)] = r
		order = append(order, string(r.ID))
	}
	if want := "1 2 3 4 5 6 7 \"x\" 8 9 null"; strings.Join(order, " ") != want {
		t.Fatalf("answered %v, want %s (none for the notification)", order, want)
	}
	text := func(id string) string {
		if c := got[id].Result.Content; len(c) == 1 {
			return c[0].Text
		}
		return ""
	}
	schema := func(name string) map[string]any {
		for _, tl := range got["2"].Result.Tools {
			if tl.Name == name {
				return tl.InputSchema
			}
		}
		return nil
	}
	if s := schema("commits"); fmt.Sprint(s["required"]) != "[repository]" || len(s["properties"].(map[string]any)) != 6 {
		t.Errorf("commits schema = %v", s)
	}
	if s := schema("work"); s["required"] != nil || len(s["properties"].(map[string]any)) != 3 {
		t.Errorf("work schema = %v", s)
	}
	if v := got["1"].Result.ProtocolVersion; v != "2025-06-18" {
		t.Errorf("protocol version %q, want the client's", v)
	}
	if v := got["9"].Result.ProtocolVersion; v != mcpVersions[0] {
		t.Errorf("unknown protocol version answered with %q, want the newest", v)
	}
	if tools := got["2"].Result.Tools; len(tools) != len(mcpTools) || len(tools) < 6 || tools[0].Annotations["readOnlyHint"] != true || tools[1].InputSchema["type"] != "object" {
		t.Errorf("tools/list = %+v", tools)
	}
	if s := text("3"); !strings.Contains(s, `"name":"app"`) || !strings.Contains(s, `"repository":"me/app"`) ||
		!strings.Contains(s, `"commits":1`) || strings.Contains(s, "/home/me") || got["3"].Result.IsError {
		t.Errorf("status = %s", s)
	}
	if s := text("4"); !strings.Contains(s, `"devTypeTitle":"KEEPER"`) || !strings.Contains(s, `"agilityStat":50`) ||
		!strings.Contains(s, `"reasoning":{"agilityReasoning":"You commit on few days.","devTypeDescriptionAi":"Steady with AI."}`) ||
		strings.Contains(s, "shareCode") {
		t.Errorf("devtype = %s", s)
	}
	if s := text("5"); got["5"].Result.IsError || !strings.Contains(s, `"name":"Go"`) || !strings.Contains(s, `"projectCount":1`) {
		t.Errorf("stack worked out here = %s (error %v)", s, got["5"].Result.IsError)
	}
	if s := text("6"); !strings.HasPrefix(s, "# Development portfolio") || !strings.Contains(s, "**KEEPER**") || !strings.Contains(s, "### app") {
		t.Errorf("portfolio = %s", s)
	}
	for id, code := range map[string]int{"7": -32602, "8": -32601, "null": -32700} {
		if e := got[id].Error; e == nil || e.Code != code {
			t.Errorf("id %s: error %+v, want %d", id, e, code)
		}
	}
	if got[`"x"`].Error != nil {
		t.Error("ping failed")
	}
}

// The plugin for Claude Code and Codex (plugins/gitfolio) runs this binary's mcp command, and both
// manifests and marketplaces name the same plugin and version.
func TestPluginManifests(t *testing.T) {
	read := func(p string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return v
	}
	claude, codex := read("plugins/gitfolio/.claude-plugin/plugin.json"), read("plugins/gitfolio/plugin.json")
	if claude["name"] != "gitfolio" || codex["name"] != "gitfolio" || claude["version"] != codex["version"] || claude["version"] == nil {
		t.Errorf("manifests: %v %v / %v %v", claude["name"], claude["version"], codex["name"], codex["version"])
	}
	server := read("plugins/gitfolio/mcp.json")["mcpServers"].(map[string]any)["gitfolio"].(map[string]any)
	if server["command"] != "gitfolio" || fmt.Sprint(server["args"]) != "[mcp]" || !slices.Contains(commands, "mcp") {
		t.Errorf("mcp.json runs %v %v", server["command"], server["args"])
	}
	cm := read(".claude-plugin/marketplace.json")["plugins"].([]any)[0].(map[string]any)
	xm := read(".agents/plugins/marketplace.json")["plugins"].([]any)[0].(map[string]any)
	if cm["name"] != "gitfolio" || cm["source"] != "./plugins/gitfolio" || xm["name"] != "gitfolio" ||
		xm["source"].(map[string]any)["path"] != "./plugins/gitfolio" {
		t.Errorf("marketplaces: %v / %v", cm, xm)
	}
}

// What AI tools see follows config ai, set by a person (CLI) or by the AI tool for them (set_ai_exposure),
// and no tool shows a hidden repository, a name replaced by another, a path, a branch, a hash or an email.
func TestAIExposure(t *testing.T) {
	f := &fakeAline{token: testToken, devType: map[string]any{"devTypeTitle": "KEEPER"}, noStats: true}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut = o, e }(out, errOut)
	out, errOut = &buf, &buf
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}))
	must(saveRepos(dir, []Repo{
		{ID: "a", Name: "acme-payments", Path: "/work/acme-payments", Provider: "GITHUB", Namespace: "acme/acme-payments"},
		{ID: "b", Name: "secret-proj", Path: "/work/secret-proj", Provider: "GITLAB", Namespace: "corp/secret-proj"},
		{ID: "c", Name: "side", Path: "/home/me/side", Provider: "GITHUB", Namespace: "me/side"},
		{ID: "d", Name: "internal-tool", Path: "/work/internal-tool", Provider: "OTHER", Namespace: "team/internal-tool"},
	}))
	commit := func(repo, hash string) Commit {
		return Commit{Repo: repo, Hash: hash, Branch: "feature/acme-billing", AuthorEmail: "dev@example.com", Date: "2026-09-01T12:00:00Z",
			Message: "feat: acme billing", CreationType: creationHuman, Files: []FileStat{{Name: "billing/acme/invoice.go", Add: 3}}}
	}
	must(writeCommits(dir, []Commit{commit("a", "a1b2c3d4"), commit("b", "b1b2c3d4"), commit("c", "c1b2c3d4"), commit("d", "d1b2c3d4")}))

	must(cmdConfigAI(dir, []string{"name", "acme-payments", "Payments", "backend"}))
	must(cmdConfigAI(dir, []string{"hide", "secret-proj"}))
	must(cmdConfigAI(dir, []string{"messages", "side", "subjects"}))
	if err := cmdConfigAI(dir, []string{"messages", "side", "all"}); err == nil {
		t.Error("an unknown messages setting was taken")
	}
	if err := cmdConfigAI(dir, []string{"nme", "side", "x"}); err == nil || !strings.Contains(err.Error(), "`gitfolio config ai name`") {
		t.Errorf("mistyped config ai word: %v", err)
	}
	buf.Reset()
	must(cmdConfigAI(dir, nil))
	for _, want := range []string{"acme-payments", "as Payments backend, commit messages off", "secret-proj", "hidden from AI tools", "as side, commit messages subjects"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("config ai shows no %q:\n%s", want, buf.String())
		}
	}
	// For a program: every repository in config ai's words, after a change too.
	v, err := jsonStdout(t, func() error { return cmdConfig(dir, []string{"ai", "branches", "internal-tool", "on", "--json"}) })
	jsonOut = false
	got := fmt.Sprint(v["repositories"])
	if err != nil || got != "[map[branches:off hidden:false messages:off name:Payments backend path:/work/acme-payments repository:acme-payments] "+
		"map[branches:off hidden:true messages:off name:real path:/work/secret-proj repository:secret-proj] "+
		"map[branches:off hidden:false messages:subjects name:real path:/home/me/side repository:side] "+
		"map[branches:on hidden:false messages:off name:real path:/work/internal-tool repository:internal-tool]]" {
		t.Errorf("config ai --json: %v\n%s", err, got)
	}
	// AI tools name a repository as they see it, or by a path in it: its own name finds nothing once it
	// shows under another name or is hidden, so it cannot tell what those are called.
	if _, err := mcpSetExposure(dir, map[string]any{"repository": "secret-proj", "hidden": false}); err == nil {
		t.Error("a hidden repository was found, and shown again, by its own name")
	}
	if _, err := mcpWork(dir, map[string]any{"repository": "acme-payments"}); err == nil {
		t.Error("a renamed repository was found by its own name")
	}
	if _, err := mcpWork(dir, map[string]any{"repository": "Payments backend"}); err != nil {
		t.Errorf("by the name AI tools see: %v", err)
	}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_ai_exposure","arguments":{"repository":"internal-tool","name":"anonymous","branches":false}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"set_ai_exposure","arguments":{"repository":"side","messages":"everything"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ai_settings"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"status"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"portfolio"}}`,
	}, "\n")
	var res bytes.Buffer
	must(serveMCP(dir, strings.NewReader(in), &res))
	text := map[int]string{}
	errs := map[int]bool{}
	dec := json.NewDecoder(&res)
	for dec.More() {
		var r struct {
			ID     int
			Result struct {
				Content []struct{ Text string }
				IsError bool
			}
		}
		must(dec.Decode(&r))
		text[r.ID], errs[r.ID] = r.Result.Content[0].Text, r.Result.IsError
	}
	if errs[1] || !errs[2] {
		t.Errorf("set_ai_exposure: %s / %s", text[1], text[2])
	}
	if s := text[3]; !strings.Contains(s, `"repository":"Payments backend","name":"alias"`) || !strings.Contains(s, `"repository":"Repository 1","name":"anonymous"`) ||
		!strings.Contains(s, `"repository":"side","name":"real","messages":"subjects"`) || !strings.Contains(s, `"hidden":1`) {
		t.Errorf("ai_settings = %s", s)
	}
	if s := text[4]; !strings.Contains(s, `"name":"Payments backend"`) || !strings.Contains(s, `"repository":"me/side"`) || strings.Count(s, `"commits":1`) != 3 {
		t.Errorf("status = %s", s)
	}
	if s := text[5]; !strings.Contains(s, "### Payments backend") || !strings.Contains(s, "### Repository 1") || !strings.Contains(s, "commits: 3") {
		t.Errorf("portfolio = %s", s)
	}
	for id := 3; id <= 5; id++ {
		for _, leak := range []string{"acme-payments", "acme/", "secret-proj", "corp/", "internal-tool", "team/", "/work", "/home",
			"feature/", "billing", "invoice", "a1b2c3d4", "dev@example.com"} {
			if strings.Contains(text[id], leak) {
				t.Errorf("tool %d shows %q:\n%s", id, leak, text[id])
			}
		}
	}
}

// An AI tool starts GitFolio for the user: policy agreed first, the user's own email and code, a
// repository, the sync mode, and a work email.
func TestMCPSetup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &fakeAline{}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(o io.Writer) { out = o }(out)
	out = io.Discard
	dir, repo := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "dev@example.com"},
		{"config", "user.name", "dev"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		if b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	call := func(name string, args map[string]any) (map[string]any, error) {
		t.Helper()
		i := slices.IndexFunc(mcpTools, func(x mcpTool) bool { return x.Name == name })
		v, err := mcpTools[i].run(dir, args)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m, nil
	}
	if _, err := call("add_repository", map[string]any{"path": repo}); err == nil || !strings.Contains(err.Error(), "login_start") {
		t.Errorf("a repository was added before logging in: %v", err)
	}
	if _, err := call("login_start", map[string]any{"email": "dev@example.com"}); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("login without agreeing: %v", err)
	}
	if p, _ := call("policy", nil); !strings.Contains(fmt.Sprint(p["dataPolicy"]), "Source code is never collected") ||
		!strings.Contains(fmt.Sprint(p["setupActions"]), "git hooks (post-commit, pre-push)") {
		t.Errorf("policy = %v", p)
	}
	if slices.ContainsFunc(mcpTools, func(x mcpTool) bool { return x.Name == "email_verify" }) {
		t.Error("email_verify takes a code in the conversation")
	}
	s, err := call("login_start", map[string]any{"email": "Dev@Example.com", "agreed": true})
	enter, _ := s["enterCode"].(map[string]any)
	if err != nil || s["newAccount"] != true || s["codeDigits"] != float64(6) || enter["here"] != "login_verify" ||
		!strings.HasPrefix(fmt.Sprint(enter["terminal"]), "gitfolio login --email dev@example.com --code <code> --signup") ||
		!strings.Contains(fmt.Sprint(s["signupNotice"]), "dev@example.com") {
		t.Fatalf("login_start = %v, %v", s, err)
	}
	if _, err := call("login_verify", map[string]any{"email": "dev@example.com", "code": "12345"}); errorCode(err) != "codeFormat" {
		t.Errorf("short code: %v", err)
	}
	if _, err := call("login_verify", map[string]any{"email": "dev@example.com", "code": "000000", "signup": true}); err == nil ||
		!strings.Contains(err.Error(), "wrong code") {
		t.Errorf("wrong code: %v", err)
	}
	if _, err := call("login_verify", map[string]any{"email": "dev@example.com", "code": "123456"}); err == nil ||
		!strings.Contains(err.Error(), "Terms of Service") {
		t.Errorf("a new account without agreeing: %v", err)
	}
	v, err := call("login_verify", map[string]any{"email": "dev@example.com", "code": "123456", "signup": true, "notifications": true})
	if err != nil || v["loggedIn"] != true || v["newAccount"] != true || v["webLogin"] == nil || f.notified == nil || !*f.notified {
		t.Fatalf("login_verify = %v, %v (notifications %v)", v, err, f.notified)
	}
	if again, _ := call("login_start", map[string]any{"email": "dev@example.com", "agreed": true}); again["loggedIn"] != true {
		t.Errorf("logging in again = %v", again)
	}
	a, err := call("add_repository", map[string]any{"path": repo})
	if err != nil || a["repository"] != filepath.Base(repo) || !strings.Contains(fmt.Sprint(a["next"]), "set_ai_exposure") ||
		!strings.HasPrefix(fmt.Sprint(a["hooksInstalled"]), "post-commit and pre-push") {
		t.Errorf("add_repository = %v, %v", a, err)
	}
	if hookStatus(repo) != "on" {
		t.Error("hooks were not installed")
	}
	if _, err := call("set_sync_mode", map[string]any{"mode": "off"}); err == nil {
		t.Error("an unknown sync mode was taken")
	}
	if _, err := call("set_sync_mode", map[string]any{"mode": "local"}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := loadConfig(dir); !cfg.AutoSyncOff || !cfg.LocalModeKnown {
		t.Errorf("local mode not saved: %+v", cfg)
	}
	if _, err := call("email_start", map[string]any{"email": "1+dev@users.noreply.github.com"}); errorCode(err) != "emailNoreply" {
		t.Errorf("email_start on a noreply address: %v", err)
	}
	s, err = call("email_start", map[string]any{"email": "work@example.com"})
	enter, _ = s["enterCode"].(map[string]any)
	if err != nil || s["codeSentTo"] != "work@example.com" || enter["terminal"] != "gitfolio email verify work@example.com --code <code>" {
		t.Fatalf("email_start = %v, %v", s, err)
	}
	v, err = jsonStdout(t, func() error {
		return cmdEmail(dir, []string{"verify", "work@example.com", "--code", "123456", "--json"})
	})
	jsonOut = false
	list, _ := v["emails"].([]any)
	if err != nil || !slices.ContainsFunc(list, func(e any) bool {
		m, _ := e.(map[string]any)
		return m["email"] == "work@example.com" && m["verified"] == true
	}) {
		t.Errorf("email verify --code after email_start = %v, %v", v, err)
	}
	if cfg, _ := loadConfig(dir); !slices.Contains(cfg.Emails, "work@example.com") {
		t.Errorf("work email not kept: %v", cfg.Emails)
	}
}

// A code an AI tool sent (login_start) is entered in the app or a terminal just as well: login --code
// finishes it, and a new account first gets what the sending run would have shown, the code kept.
func TestLoginCodeFromAITool(t *testing.T) {
	f := &fakeAline{}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(o io.Writer) { out, jsonOut = o, false }(out)
	out = io.Discard
	dir := t.TempDir()
	if _, err := mcpLoginStart(dir, map[string]any{"email": "dev@example.com", "agreed": true}); err != nil {
		t.Fatal(err)
	}
	_, err := jsonStdout(t, func() error {
		return cmdLogin(dir, []string{"--email", "dev@example.com", "--code", "123456", "--json"})
	})
	if e := errorJSON(err); e["code"] != "signupConsent" || !strings.Contains(e["signupNotice"], "dev@example.com") || e["notifyQuestion"] == "" {
		t.Errorf("login --code without --signup = %v", e)
	}
	v, err := jsonStdout(t, func() error {
		return cmdLogin(dir, []string{"--email", "dev@example.com", "--code", "123456", "--signup", "--notify", "--json"})
	})
	if err != nil || v["created"] != true || f.notified == nil || !*f.notified {
		t.Fatalf("login --code after login_start = %v, %v (notifications %v)", v, err, f.notified)
	}
}

// AI tools get the tech stack of the repositories they may see: a hidden one counts for nothing.
func TestMCPStackLeavesHiddenOut(t *testing.T) {
	dir := t.TempDir()
	if err := saveRepos(dir, []Repo{{ID: "a", Name: "app"}, {ID: "s", Name: "secret", AI: aiExposure{Hidden: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeCommits(dir, []Commit{
		{Repo: "a", Hash: "1", Date: "2026-09-01T12:00:00Z", Message: "feat: app", Files: []FileStat{{Name: "main.go", Add: 1}}},
		{Repo: "s", Hash: "2", Date: "2026-09-02T12:00:00Z", Message: "feat: secret", Files: []FileStat{{Name: "main.rs", Add: 1}}},
	}); err != nil {
		t.Fatal(err)
	}
	names := func(ai bool) string {
		u, err := techUsagesNow(dir, "en", ai)
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, l := range u.Languages {
			n = append(n, l.Name)
		}
		return strings.Join(n, " ")
	}
	if all, ai := names(false), names(true); all != "Go Rust" || ai != "Go" {
		t.Errorf("stack: %q here, %q for AI tools", all, ai)
	}
}

// The status tool gives the last sync error masked: a failed deletion names the URL of a repository the
// user may have hidden from AI tools.
func TestMCPStatusMasksSyncError(t *testing.T) {
	dir := t.TempDir()
	if err := saveSync(dir, syncState{LastError: `Delete "https://aline.team/api/cli/repositories?namespace=corp%2Fsecret-proj": dial tcp: lookup aline.team: no such host`}); err != nil {
		t.Fatal(err)
	}
	v, err := mcpStatus(dir, nil)
	got := fmt.Sprint(v.(map[string]any)["lastSyncError"])
	if err != nil || strings.Contains(got, "secret-proj") || !strings.Contains(got, "[URL]") || !strings.Contains(got, "no such host") {
		t.Errorf("lastSyncError = %q, %v", got, err)
	}
}
