package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAPIBase(t *testing.T) {
	for url, ok := range map[string]bool{
		"":                            true, // default
		"https://staging.example.com": true,
		"http://127.0.0.1:8080":       true,
		"http://localhost:8080":       true,
		"http://aline.team":           false,
		"ftp://aline.team":            false,
		"aline.team":                  false,
	} {
		t.Setenv("GITFOLIO_API_URL", url)
		if _, err := apiBase(""); (err == nil) != ok {
			t.Errorf("apiBase with env %q: err = %v, want ok=%v", url, err, ok)
		}
	}
	// The environment wins over the saved setting, which wins over production.
	t.Setenv("GITFOLIO_API_URL", "")
	if got, _ := apiBase("https://staging.example.com/api/"); got != "https://staging.example.com/api" {
		t.Errorf("configured base = %q", got)
	}
	if got, _ := apiBase(""); got != defaultAPIBase {
		t.Errorf("default base = %q", got)
	}
	t.Setenv("GITFOLIO_API_URL", "http://127.0.0.1:9")
	if got, _ := apiBase("https://staging.example.com/api"); got != "http://127.0.0.1:9" {
		t.Errorf("env should override config, got %q", got)
	}
	// Release builds use production whatever is set, and refuse to change the server.
	defer func(v string) { version = v }(version)
	version = "1.0.0"
	if got, _ := apiBase("https://staging.example.com/api"); got != defaultAPIBase {
		t.Errorf("release build base = %q", got)
	}
	if err := cmdConfig(t.TempDir(), []string{"api-url", "https://staging.example.com/api"}); err == nil {
		t.Error("release build accepted config api-url")
	}
}

// fakeAline follows the aline.team server contract (ApiBody envelope, ErrorCode codes)
// closely enough to check what the CLI sends and how it handles the answers.
type fakeAline struct {
	token    string
	exists   bool  // the email already has an account
	verified bool  // verify was called
	notified *bool // isNotified as sent in verify; nil when left out

	startDeviceShown bool // start carried a device the code email can show

	work []string // work emails verified with a code (lowercased, in order)
	// waitFor maps a namespace to the email aline.team wants verified before it takes its commits (A013).
	waitFor map[string]string
	noMove  bool   // an older server without the move call
	moves   int    // repository moves made
	pend    string // the work email a code was sent to
	// devType is what GET /cli/devtype answers (nil: the empty shell, nothing to analyse yet).
	devType      map[string]any
	devTypeCalls int
	devTypeLang  string              // the Accept-Language it was last asked in
	devTypeWait  time.Duration       // how long it takes to make (longer than the client waits: a timeout)
	deps         map[string][]string // PUT /cli/dependencies: provider/namespace → "ecosystem:name"
	depsCalls    int

	// Data API. The handler holds mu; tests lock it to read.
	mu          sync.Mutex
	commits     map[string]Commit   // provider/namespace/hash → record
	stats       map[string]snapshot // provider/namespace → snapshot (PUT /cli/stats)
	newerFormat bool                // answer PUT /cli/stats with A014
	noStats     bool                // an older server: no snapshot path (404)
	modified    map[string]string   // provider/namespace/name → modifiedAt
	batches     int                 // accepted commit batches
	down        bool                // answer data calls with 503

	// GitHub device flows: the user approves on the second look (the first is PENDING).
	noGithub bool // aline.team cannot ask GitHub (EX001)
	ghLooks  int  // status looks so far, for the flow under way
	ghSignup bool // the login flow under way may sign up
}

// me is the body of GET /cli/me: the account email first, then the verified work emails.
func (f *fakeAline) me() map[string]any {
	return map[string]any{"account": map[string]any{"id": testAccountID, "email": "dev@example.com"}, "verifiedEmails": append([]string{"dev@example.com"}, f.work...)}
}

var emailDeviceName = regexp.MustCompile(`^(macOS|Linux|Windows) [A-Za-z0-9_]{1,16}$`)

const testToken = "aln_cli_token-of-the-fake-server-for-tests"

// testAccountID is a UUID string, not a number, as the server sends it.
const testAccountID = "d3bbee5d-84fc-422b-97fc-a021db3f7f66"

func (f *fakeAline) handler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, contents any) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{"success": true}
		if contents != nil {
			body["contents"] = contents
		}
		json.NewEncoder(w).Encode(body)
	}
	fail := func(w http.ResponseWriter, status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{
			"status": status, "code": code, "messageKo": "오류 " + code, "messageEn": "error " + code, "messageJa": "エラー " + code,
		}})
	}
	authed := func(r *http.Request) bool { return f.token != "" && r.Header.Get("Authorization") == "Bearer "+f.token }

	mux.HandleFunc("POST /cli/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email  string
			Device struct{ Name, Language, Timezone string }
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Email == "" {
			fail(w, 400, "C001")
			return
		}
		// The server prints the device in the code email only in this exact form (no free text).
		f.startDeviceShown = emailDeviceName.MatchString(in.Device.Name) && in.Device.Language != ""
		ok(w, map[string]any{"challengeId": "ch_1", "expiresAt": time.Now().UTC().Add(10 * time.Minute), "codeLength": 6, "accountExists": f.exists})
	})
	mux.HandleFunc("POST /cli/verify", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ChallengeID string `json:"challengeId"`
			Code        string `json:"code"`
			Device      struct{ Name, Language, Timezone string }
			IsNotified  *bool `json:"isNotified"`
		}
		f.verified = true
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.ChallengeID != "ch_1" || in.Device.Name == "" ||
			(in.Device.Language != "" && in.Device.Language != "KO" && in.Device.Language != "EN" && in.Device.Language != "JA") {
			fail(w, 400, "C001") // the server takes upper-case KO/EN/JA only
			return
		}
		f.notified = in.IsNotified
		if in.Code != "123456" {
			fail(w, 400, "A008")
			return
		}
		f.token = testToken
		ok(w, map[string]any{
			"token": f.token, "tokenExpiresAt": time.Now().UTC().Add(90 * 24 * time.Hour),
			"account":        map[string]any{"id": testAccountID, "email": "dev@example.com", "created": !f.exists},
			"verifiedEmails": []string{"dev@example.com"},
		})
	})
	ghStart := func(w http.ResponseWriter) {
		if f.noGithub {
			fail(w, 503, "EX001")
			return
		}
		f.ghLooks = 0
		ok(w, map[string]any{"connectId": "c_1", "userCode": "WDJB-MJHT", "verificationUri": "https://github.com/login/device",
			"expiresAt": time.Now().UTC().Add(15 * time.Minute), "interval": 5})
	}
	mux.HandleFunc("POST /cli/login/github", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Device     struct{ Name, Language string }
			Signup     bool  `json:"signup"`
			IsNotified *bool `json:"isNotified"`
		}
		if r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&in) != nil || in.Device.Name == "" {
			fail(w, 400, "C001") // a GitHub login carries no token: a wrong one would be refused
			return
		}
		f.ghSignup, f.notified = in.Signup, in.IsNotified
		ghStart(w)
	})
	mux.HandleFunc("GET /cli/login/github/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch f.ghLooks++; {
		case r.PathValue("id") != "c_1":
			ok(w, map[string]any{"status": "EXPIRED", "interval": 5})
		case f.ghLooks == 1:
			ok(w, map[string]any{"status": "PENDING", "interval": 10}) // GitHub asked to slow down
		case !f.exists && !f.ghSignup:
			ok(w, map[string]any{"status": "NO_ACCOUNT", "interval": 10})
		default:
			f.token = testToken
			ok(w, map[string]any{"status": "LOGGED_IN", "interval": 10, "login": "octocat",
				"token": f.token, "tokenExpiresAt": time.Now().UTC().Add(90 * 24 * time.Hour),
				"account":        map[string]any{"id": testAccountID, "email": "dev@example.com", "created": !f.exists},
				"verifiedEmails": []string{"dev@example.com"}})
		}
	})
	mux.HandleFunc("POST /cli/github", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			fail(w, 401, "A001")
			return
		}
		ghStart(w)
	})
	mux.HandleFunc("GET /cli/github/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch f.ghLooks++; {
		case !authed(r):
			fail(w, 401, "A001")
		case f.ghLooks == 1:
			ok(w, map[string]any{"status": "PENDING", "interval": 5})
		default: // the verified emails of the GitHub account, and its noreply address, are work emails now
			f.work = append(f.work, "dev@work.example", "583231+octocat@users.noreply.github.com")
			ok(w, map[string]any{"status": "CONNECTED", "interval": 5, "login": "octocat", "emails": []string{"Dev@Work.example"}})
		}
	})
	mux.HandleFunc("GET /cli/me", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			fail(w, 401, "A001")
			return
		}
		ok(w, f.me())
	})
	mux.HandleFunc("POST /cli/emails", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Email string }
		switch {
		case !authed(r):
			fail(w, 401, "A001")
		case json.NewDecoder(r.Body).Decode(&in) != nil || !validEmail(in.Email):
			fail(w, 400, "C001")
		case slices.Contains(f.me()["verifiedEmails"].([]string), strings.ToLower(in.Email)):
			fail(w, 409, "A011")
		default:
			f.pend = strings.ToLower(in.Email)
			ok(w, map[string]any{"challengeId": "ch_e", "expiresAt": time.Now().UTC().Add(10 * time.Minute), "codeLength": 6})
		}
	})
	mux.HandleFunc("POST /cli/emails/verify", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ChallengeID, Code string }
		switch {
		case !authed(r):
			fail(w, 401, "A001")
		case json.NewDecoder(r.Body).Decode(&in) != nil || in.ChallengeID != "ch_e" || f.pend == "":
			fail(w, 400, "A009")
		case in.Code != "123456":
			fail(w, 400, "A008")
		case len(f.work) >= 20: // checked again when the code is used (codes asked for earlier)
			fail(w, 400, "A012")
		default:
			f.work, f.pend = append(f.work, f.pend), ""
			ok(w, f.me())
		}
	})
	mux.HandleFunc("DELETE /cli/emails", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			fail(w, 401, "A001")
			return
		}
		f.work = slices.DeleteFunc(f.work, func(e string) bool { return e == strings.ToLower(r.URL.Query().Get("email")) })
		ok(w, f.me())
	})
	mux.HandleFunc("POST /cli/logout", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			fail(w, 401, "A001")
			return
		}
		f.token = ""
		ok(w, nil) // 200 {"success": true}
	})

	data := func(h func(w http.ResponseWriter, r *http.Request)) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case !authed(r):
				fail(w, 401, "A001")
			case f.down:
				w.WriteHeader(http.StatusServiceUnavailable) // no ApiBody, as from a proxy
			default:
				if f.commits == nil {
					f.commits, f.modified = map[string]Commit{}, map[string]string{}
				}
				h(w, r)
			}
		}
	}
	mux.HandleFunc("GET /cli/devtype", data(func(w http.ResponseWriter, r *http.Request) {
		f.devTypeCalls++
		f.devTypeLang = r.Header.Get("Accept-Language")
		time.Sleep(f.devTypeWait)
		if r.URL.Query().Get("yearPeriod") == "" {
			fail(w, 400, "C001")
			return
		}
		if f.devType == nil {
			ok(w, map[string]any{"devTypeTitle": nil})
			return
		}
		ok(w, f.devType)
	}))
	mux.HandleFunc("POST /cli/commits/batch", data(func(w http.ResponseWriter, r *http.Request) {
		var in commitBatch // one repository per request
		if r.ContentLength > 100<<20 || json.NewDecoder(r.Body).Decode(&in) != nil || in.Namespace == "" || in.AuthorEmail == "" || len(in.Commits) == 0 || len(in.Commits) > batchSize || len(in.ModifiedFiles) > maxModified {
			fail(w, 400, "C001")
			return
		}
		if e := f.waitFor[in.Namespace]; e != "" && !slices.Contains(f.me()["verifiedEmails"].([]string), e) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{
				"status": 403, "code": "A013", "email": e, "messageEn": "verify " + e + " first",
			}})
			return
		}
		for _, c := range in.Commits {
			if c.Message == "bad" || c.Namespace != "" || c.Repo != "" || c.AuthorEmail != "" || c.Hash == "" || c.Branch == "" || len(c.Files) > maxFilesSent {
				fail(w, 400, "C001") // one bad record fails the whole batch
				return
			}
		}
		for _, c := range in.Commits {
			c.Provider, c.Namespace, c.AuthorEmail = in.Provider, in.Namespace, in.AuthorEmail
			f.commits[commitKey(c)] = c
		}
		for _, m := range in.ModifiedFiles {
			f.modified[in.Provider+"/"+in.Namespace+"/"+m.Name] = m.ModifiedAt
		}
		f.batches++
		ok(w, map[string]any{"upserted": len(in.Commits)})
	}))
	mux.HandleFunc("PUT /cli/stats", data(func(w http.ResponseWriter, r *http.Request) {
		if f.noStats {
			fail(w, 404, "C004")
			return
		}
		var in snapshot
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			fail(w, 400, "C001")
			return
		}
		if f.newerFormat {
			fail(w, 400, "A014")
			return
		}
		if at, err := time.Parse(time.RFC3339, in.AsOf); err == nil && at.After(time.Now().Add(24*time.Hour)) {
			fail(w, 400, "A018")
			return
		}
		if e := f.waitFor[in.Namespace]; e != "" && !slices.Contains(f.me()["verifiedEmails"].([]string), e) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{
				"status": 403, "code": "A013", "email": e, "messageEn": "verify " + e + " first",
			}})
			return
		}
		if f.stats == nil {
			f.stats = map[string]snapshot{}
		}
		f.stats[in.Provider+"/"+in.Namespace] = in
		ok(w, nil)
	}))
	mux.HandleFunc("PUT /cli/dependencies", data(func(w http.ResponseWriter, r *http.Request) {
		f.depsCalls++
		var in struct {
			Provider, Namespace string
			Dependencies        []struct{ Ecosystem, Name string }
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Namespace == "" || in.Dependencies == nil || len(in.Dependencies) > 5000 {
			fail(w, 400, "C001")
			return
		}
		list := []string{}
		for _, d := range in.Dependencies {
			if !slices.Contains([]string{"npm", "go", "maven", "pypi", "cargo"}, d.Ecosystem) || d.Name == "" || len(d.Name) > 214 {
				fail(w, 400, "C001")
				return
			}
			list = append(list, d.Ecosystem+":"+d.Name)
		}
		if f.deps == nil {
			f.deps = map[string][]string{}
		}
		f.deps[in.Provider+"/"+in.Namespace] = list
		ok(w, nil)
	}))
	mux.HandleFunc("DELETE /cli/repositories", data(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("provider") + "/" + r.URL.Query().Get("namespace")
		n := 0
		for k := range f.commits {
			if strings.HasPrefix(k, key+"/") {
				delete(f.commits, k)
				n++
			}
		}
		ok(w, map[string]any{"deletedCommits": n})
	}))
	mux.HandleFunc("POST /cli/repositories/move", data(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Provider, Namespace, NewProvider, NewNamespace string }
		if f.noMove {
			w.WriteHeader(http.StatusNotFound) // no ApiBody, as for a route the server does not have
			return
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Namespace == "" || in.NewNamespace == "" {
			fail(w, 400, "C001")
			return
		}
		from, to := in.Provider+"/"+in.Namespace+"/", in.NewProvider+"/"+in.NewNamespace+"/"
		n := 0
		for k, c := range f.commits {
			if hash, ok := strings.CutPrefix(k, from); ok {
				c.Provider, c.Namespace = in.NewProvider, in.NewNamespace
				f.commits[to+hash] = c
				delete(f.commits, k)
				n++
			}
		}
		f.moves++
		ok(w, map[string]any{"result": map[bool]string{true: "MOVED", false: "NOOP"}[n > 0]})
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
}

func TestSignInMeLogout(t *testing.T) {
	f := &fakeAline{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)
	dir := t.TempDir()

	c, err := newClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITFOLIO_LANG", "ko")       // sent as device.language "KO"
	codes := []string{"000000", "123456"} // a wrong code (A008) first, then the right one
	asked := false
	res, err := c.signIn("dev@example.com", func() (bool, bool) { asked = true; return true, true }, func(retry bool, length int) string {
		code := codes[0]
		codes = codes[1:]
		return code
	})
	if err != nil || !res.Account.Created || !asked {
		t.Fatalf("signIn = %+v, %v (sign-up confirmed: %v)", res, err, asked)
	}
	if f.notified == nil || !*f.notified {
		t.Errorf("isNotified = %v, want true for a sign-up that opted in", f.notified)
	}
	if !f.startDeviceShown {
		t.Errorf("start did not send a device the code email can show (name %q)", deviceName())
	}
	if saved, _ := loadCredentials(dir); saved.Token != testToken || saved.Email != "dev@example.com" {
		t.Fatalf("credentials after sign-in = %+v", saved)
	}

	var me struct {
		Account struct{ Email string } `json:"account"`
	}
	if err := c.call("GET", "/cli/me", nil, &me); err != nil || me.Account.Email != "dev@example.com" {
		t.Fatalf("GET /cli/me = %+v, %v", me, err)
	}

	if err := cmdWhoami(dir, nil); err != nil {
		t.Fatalf("whoami: %v", err)
	}

	if err := cmdLogout(dir, nil); err != nil {
		t.Fatal(err)
	}
	if f.token != "" {
		t.Error("logout did not revoke the token on the server")
	}
	if saved, _ := loadCredentials(dir); !reflect.DeepEqual(saved, Credentials{}) {
		t.Errorf("credentials after logout = %+v, want empty", saved)
	}

	// A login aline.team no longer takes is not renewed only to be revoked: at a terminal, no code is
	// asked for, and nothing is warned about.
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer, i func() bool, r *bufio.Reader) { out, errOut, interactive, stdin = o, e, i, r }(out, errOut, interactive, stdin)
	out, errOut, interactive, stdin = &buf, &buf, func() bool { return true }, bufio.NewReader(strings.NewReader(""))
	if err := saveCredentials(dir, Credentials{Token: "no longer taken", Email: "dev@example.com", Server: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := cmdLogout(dir, nil); err != nil || strings.Contains(buf.String(), "has expired") || strings.Contains(buf.String(), "Could not log out") {
		t.Errorf("logout with a login no longer taken: %v\n%s", err, buf.String())
	}
	if saved, _ := loadCredentials(dir); saved.Token != "" {
		t.Errorf("token kept: %+v", saved)
	}
}

// An existing account signs in without the sign-up notice; declining the notice for a new email
// sends no code and makes no account.
func TestSignupConfirmation(t *testing.T) {
	for _, tc := range []struct {
		exists, confirm, wantAsked, wantVerify bool
	}{
		{exists: true, confirm: false, wantAsked: false, wantVerify: true},
		{exists: false, confirm: false, wantAsked: true, wantVerify: false},
	} {
		f := &fakeAline{exists: tc.exists}
		srv := httptest.NewServer(f.handler())
		t.Setenv("GITFOLIO_API_URL", srv.URL)
		c, err := newClient(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		asked := false
		_, err = c.signIn("dev@example.com", func() (bool, bool) { asked = true; return tc.confirm, true }, func(bool, int) string { return "123456" })
		srv.Close()
		if asked != tc.wantAsked || f.verified != tc.wantVerify {
			t.Errorf("exists=%v: notice shown %v, verify called %v (err %v)", tc.exists, asked, f.verified, err)
		}
		if tc.exists && f.notified != nil {
			t.Errorf("isNotified sent for an existing account: %v", *f.notified)
		}
		if !tc.wantVerify && err != errSignupCancelled {
			t.Errorf("declined sign-up returned %v, want errSignupCancelled", err)
		}
	}
}

func TestValidCode(t *testing.T) {
	for code, want := range map[string]bool{"123456": true, "01293u1": false, "12345": false, "1234567": false, "12 456": false, "": false} {
		if got := validCode(code, 6); got != want {
			t.Errorf("validCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestZoneFromPath(t *testing.T) {
	for in, want := range map[string]string{
		"/var/db/timezone/zoneinfo/Asia/Seoul":   "Asia/Seoul",
		"../usr/share/zoneinfo/America/New_York": "America/New_York",
		"/usr/share/zoneinfo/UTC":                "UTC",
		"/etc/something-else":                    "",
	} {
		if got := zoneFromPath(in); got != want {
			t.Errorf("zoneFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// A token the server rejects (A001: expired, revoked or unknown) is removed locally.
func TestRejectedTokenIsRemoved(t *testing.T) {
	f := &fakeAline{} // no valid token on the server
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)
	dir := t.TempDir()
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com"}); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = c.call("GET", "/cli/me", nil, nil)
	if ae, ok := err.(interface{ Unwrap() error }); err == nil || !ok || ae.Unwrap() == nil {
		t.Fatalf("call with a rejected token = %v, want an A001 error", err)
	}
	if saved, _ := loadCredentials(dir); saved.Token != "" {
		t.Errorf("rejected token kept: %+v", saved)
	}
}

// TestLoginAgain: when aline.team rejects the token, a person at the terminal logs in again right away
// with a code sent to the same email and the request goes through; without a terminal (hooks,
// scheduled runs) the token is removed and the error says to run `gitfolio login`.
func TestLoginAgain(t *testing.T) {
	f := &fakeAline{token: testToken, exists: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	dir := t.TempDir()
	expired := Credentials{Token: "aln_cli_expired", Email: "dev@example.com", Server: srv.URL}
	savedInteractive, savedStdin := interactive, stdin
	t.Cleanup(func() { interactive, stdin = savedInteractive, savedStdin })

	interactive = func() bool { return false }
	if err := saveCredentials(dir, expired); err != nil {
		t.Fatal(err)
	}
	c, _ := newClient(dir)
	if _, err := c.me(); err == nil || !strings.Contains(err.Error(), "gitfolio login") {
		t.Errorf("rejected token without a terminal: %v, want a login hint", err)
	}
	if creds, _ := loadCredentials(dir); creds.Token != "" || creds.Email != "dev@example.com" {
		t.Errorf("after the rejection: %+v, want no token and the email kept", creds)
	}

	interactive = func() bool { return true }
	stdin = bufio.NewReader(strings.NewReader("123456\n"))
	if err := saveCredentials(dir, expired); err != nil {
		t.Fatal(err)
	}
	c, _ = newClient(dir)
	if _, err := c.me(); err != nil {
		t.Fatalf("logging in again: %v", err)
	}
	if creds, _ := loadCredentials(dir); creds.Token != testToken {
		t.Error("the new token was not saved")
	}

	// No email to send the code to (say the login was for another server): GitHub or an email is asked for.
	stdin = bufio.NewReader(strings.NewReader("n\ndev@example.com\n123456\n"))
	if err := saveCredentials(dir, Credentials{Token: "aln_cli_expired", Server: srv.URL}); err != nil {
		t.Fatal(err)
	}
	c, _ = newClient(dir)
	if _, err := c.me(); err != nil {
		t.Fatalf("logging in with the email asked for: %v", err)
	}
	if creds, _ := loadCredentials(dir); creds.Token != testToken || creds.Email != "dev@example.com" {
		t.Errorf("after logging in: %+v", creds)
	}
}

// Only a path aline.team does not have means a server without that feature yet, not any 404.
func TestUnsupported(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&apiError{Status: 404, Code: "C004"}, true},
		{fmt.Errorf("put: %w", &apiError{Status: 404, Code: "C004"}), true},
		{&apiError{Status: 404, Code: "U002"}, false}, // an unknown user
		{&apiError{Status: 404}, false},               // a proxy page
		{&apiError{Status: 400, Code: "C004"}, false},
		{errors.New("offline"), false},
		{nil, false},
	} {
		if got := unsupported(c.err); got != c.want {
			t.Errorf("unsupported(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// A program logs in with two runs that ask nothing: --email sends the code and tells whether the
// account exists (with the notice to show before signing up), --code finishes. Signing up needs
// --signup; a wrong code can be tried again; the code waits in the data folder in between.
func TestLoginInTwoRuns(t *testing.T) {
	f := &fakeAline{}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut, jsonOut = o, e, false }(out, errOut)
	out, errOut = &buf, &buf
	dir := t.TempDir()
	login := func(args ...string) (map[string]any, error) {
		t.Helper()
		return jsonStdout(t, func() error { return cmdLogin(dir, append(args, "--json")) })
	}

	v, err := login("--email", "dev@example.com")
	if err != nil || v["accountExists"] != false || v["codeLength"] != 6.0 || !f.startDeviceShown ||
		!strings.Contains(fmt.Sprint(v["signupNotice"]), "Terms of Service") || v["notifyQuestion"] == "" {
		t.Fatalf("code sent: %v %v", v, err)
	}
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"--code", "123456"}, "signupConsent"}, // no account yet, no agreement
		{[]string{"--code", "12", "--signup"}, "codeFormat"},
		{[]string{"--code", "654321", "--signup"}, "codeWrong"},
	} {
		if _, err := login(append([]string{"--email", "dev@example.com"}, c.args...)...); errorCode(err) != c.code {
			t.Errorf("%v: %v, want %s", c.args, err, c.code)
		}
	}
	v, err = login("--email", "Dev@example.com", "--code", "123456", "--signup", "--notify") // the code waited
	if err != nil || v["created"] != true || v["email"] != "dev@example.com" || f.notified == nil || !*f.notified {
		t.Fatalf("signed up: %v %v (notified %v)", v, err, f.notified)
	}
	if creds, _ := loadCredentials(dir); creds.Token != testToken {
		t.Error("the token was not kept")
	}
	if _, ok, _ := keptCode(dir, "login", "dev@example.com"); ok {
		t.Error("the used code still waits")
	}
	if _, err := login("--email", "dev@example.com"); errorCode(err) != "alreadyLoggedIn" {
		t.Errorf("logged in already: %v", err)
	}
	if v, err := jsonStdout(t, func() error { return cmdWhoami(dir, []string{"--json"}) }); err != nil || v["loggedIn"] != true || v["email"] != "dev@example.com" {
		t.Errorf("whoami --json: %v %v", v, err)
	}
	if v, err := jsonStdout(t, func() error { return cmdLogout(dir, []string{"--json"}) }); err != nil || v["revoked"] != true {
		t.Errorf("logout --json: %v %v", v, err)
	}
	if v, err := jsonStdout(t, func() error { return cmdWhoami(dir, []string{"--json"}) }); err != nil || v["loggedIn"] != false {
		t.Errorf("whoami --json, logged out: %v %v", v, err)
	}
	if _, err := login("--email", "other@example.com", "--code", "123456"); errorCode(err) != "codeNotSent" {
		t.Errorf("no code sent: %v", err)
	}
	if _, err := login("--code", "123456"); errorCode(err) != "usage" {
		t.Errorf("--code without --email: %v", err)
	}
	for code, want := range map[string]string{codeWrongCode: "codeWrong", codeExpired: "codeExpired", codeRateLimited: "rateLimited", "U004": "failed"} {
		if got := errorCode(&apiError{Status: 400, Code: code}); got != want {
			t.Errorf("errorCode(%s) = %s, want %s", code, got, want)
		}
	}
}

// Logging in with GitHub: the code goes where the program or the person sees it, at the pace aline.team
// asks for, and the GitHub account's primary email is the account. Without one the sign-up is agreed to
// first, then a new code signs it up. When aline.team cannot ask GitHub, a person is asked for an email.
func TestGithubLogin(t *testing.T) {
	f := &fakeAline{exists: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(w func(int)) { githubWait = w }(githubWait)
	var waits []int
	githubWait = func(s int) { waits = append(waits, s) }
	defer func(o, e io.Writer, i func() bool, r *bufio.Reader) {
		out, errOut, interactive, stdin, jsonOut = o, e, i, r, false
	}(out, errOut, interactive, stdin)
	var screen bytes.Buffer
	out, errOut, interactive = &screen, &screen, func() bool { return false }
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	v, err := jsonStdout(t, func() error { return cmdLogin(dir, []string{"--github", "--json"}) })
	if err != nil || v["email"] != "dev@example.com" || v["github"] != "octocat" || v["created"] != false ||
		!strings.Contains(screen.String(), `"step":"githubCode"`) || !strings.Contains(screen.String(), `"userCode":"WDJB-MJHT"`) ||
		!slices.Equal(waits, []int{5, 10}) {
		t.Fatalf("login --github = %v, %v; waits %v\n%s", v, err, waits, screen.String())
	}
	if creds, _ := loadCredentials(dir); creds.Token != testToken || creds.Email != "dev@example.com" || creds.Server != srv.URL {
		t.Errorf("credentials: %+v", creds)
	}

	// No account: a program is told to ask for the sign-up first, then signs up with --signup.
	must(saveCredentials(dir, Credentials{}))
	f.exists = false
	_, err = jsonStdout(t, func() error { return cmdLogin(dir, []string{"--github", "--json"}) })
	if errorCode(err) != "signupConsent" || !strings.Contains(errorJSON(err)["signupNotice"], "GitHub") {
		t.Errorf("no account, no --signup: %v %v", err, errorJSON(err))
	}
	v, err = jsonStdout(t, func() error { return cmdLogin(dir, []string{"--github", "--signup", "--notify", "--json"}) })
	if err != nil || v["created"] != true || f.notified == nil || !*f.notified {
		t.Errorf("login --github --signup --notify = %v, %v (notified %v)", v, err, f.notified)
	}

	// At a terminal GitHub comes first; without an account, the notice, a yes, and a new code.
	must(saveCredentials(dir, Credentials{}))
	jsonOut, screen = false, bytes.Buffer{}
	interactive, stdin = func() bool { return true }, bufio.NewReader(strings.NewReader("\ny\nn\n"))
	c, _ := newClient(dir)
	if err := c.login("en"); err != nil || strings.Count(screen.String(), "enter the code WDJB-MJHT") != 2 ||
		!strings.Contains(screen.String(), "Terms of Service") || f.notified == nil || *f.notified {
		t.Errorf("login at a terminal: %v (notified %v)\n%s", err, f.notified, screen.String())
	}

	// aline.team cannot ask GitHub now: a program is told so; a person goes on with an email code.
	must(saveCredentials(dir, Credentials{}))
	f.noGithub, f.exists = true, true
	_, err = jsonStdout(t, func() error { return cmdLogin(dir, []string{"--github", "--json"}) })
	if errorCode(err) != "githubUnavailable" || strings.Contains(err.Error(), "gitfolio") {
		t.Errorf("GitHub unavailable: %v", err)
	}
	jsonOut = false
	stdin = bufio.NewReader(strings.NewReader("\ndev@example.com\n123456\n"))
	c, _ = newClient(dir)
	if err := c.login("en"); err != nil {
		t.Errorf("an email after GitHub could not be used: %v", err)
	}
	if creds, _ := loadCredentials(dir); creds.Token != testToken {
		t.Error("no login after the email code")
	}
}

// Connecting GitHub makes its verified emails and its noreply address verified work emails, here too.
func TestEmailGithub(t *testing.T) {
	f := &fakeAline{token: testToken}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(w func(int)) { githubWait = w }(githubWait)
	githubWait = func(int) {}
	defer func(o, e io.Writer) { out, errOut, jsonOut = o, e, false }(out, errOut)
	out, errOut = io.Discard, io.Discard
	dir := t.TempDir()
	if err := cmdEmail(dir, []string{"github", "--json"}); errorCode(err) != "loginFirst" {
		t.Errorf("email github logged out: %v", err)
	}
	jsonOut = false
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(dir, Config{Emails: []string{"dev@example.com"}}); err != nil {
		t.Fatal(err)
	}
	v, err := jsonStdout(t, func() error { return cmdEmail(dir, []string{"github", "--json"}) })
	noreply := "583231+octocat@users.noreply.github.com"
	if connected, _ := v["connected"].([]any); err != nil || v["github"] != "octocat" || len(connected) != 2 {
		t.Fatalf("email github = %v, %v", v, err)
	}
	cfg, _ := loadConfig(dir)
	creds, _ := loadCredentials(dir)
	if !slices.Equal(cfg.Emails, []string{"dev@example.com", "dev@work.example", noreply}) || !verified(creds, "dev@work.example") || !verified(creds, noreply) {
		t.Errorf("work emails %v, verified %v", cfg.Emails, creds.Verified)
	}
}
