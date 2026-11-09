package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

// defaultAPIBase is the aline.team API (the contract agreed with the aline.team server).
const defaultAPIBase = "https://aline.team/api"

// apiBase returns the API root. Release builds always use production. Builds from source (version
// sourceBuild) may point elsewhere for testing: GITFOLIO_API_URL if set, else the one saved with
// `gitfolio config api-url` (hooks started by GUI git clients do not see shell variables).
func apiBase(configured string) (string, error) {
	raw := os.Getenv("GITFOLIO_API_URL")
	if raw == "" {
		raw = configured
	}
	if raw == "" || !sourceBuild() {
		return defaultAPIBase, nil
	}
	if err := checkAPIURL(raw); err != nil {
		return "", err
	}
	return strings.TrimRight(raw, "/"), nil
}

// checkAPIURL accepts only HTTPS, except plain HTTP to this computer, so the token never travels unencrypted.
func checkAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return failure("apiURLInvalid", raw)
	}
	h := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && (h == "127.0.0.1" || h == "localhost" || h == "::1")) {
		return failure("apiURLHTTPS", raw)
	}
	return nil
}

// apiError is a failure answer: {"success": false, "error": {"status", "code", "messageKo", …}}.
// Codes are the server's ErrorCode values (A001, A008, …).
type apiError struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	MessageKo string `json:"messageKo"`
	MessageEn string `json:"messageEn"`
	MessageJa string `json:"messageJa"`
	Email     string `json:"email,omitempty"` // A013: the email to verify before this repository is taken
}

func (e *apiError) Error() string {
	msg := map[string]string{"ko": e.MessageKo, "ja": e.MessageJa}[detectLang(os.Getenv)]
	if msg == "" {
		msg = e.MessageEn
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("aline.team: %s (%s)", msg, e.Code)
}

// Server error codes the CLI reacts to.
const (
	codeTokenInvalid  = "A001" // token invalid, expired or revoked
	codeNotLoggedIn   = "A000" // no valid login: the token is unknown to this server
	codeWrongCode     = "A008" // email code does not match
	codeExpired       = "A009" // email code expired or tried too often: a new one is needed
	codeEmailVerified = "A011" // the work email is already verified for this account
	codeTooManyEmails = "A012" // the account has as many verified work emails as it may have
	codeUnverified    = "A013" // a repository's commits wait until error.email is verified (from 0.2.0)
	codeNewerFormat   = "A014" // aline.team takes a newer snapshot format: GitFolio needs an update
	codeAheadOfClock  = "A018" // a snapshot's asOf more than a day ahead of aline.team's clock
	codeRateLimited   = "R001"
	codeBadInput      = "C001" // malformed request
	codeNoPath        = "C004" // no such path: a server without that feature yet
)

// unsupported reports whether aline.team answered that it has no such path, as a server without that
// feature yet does. A 404 alone is not enough: an unknown user is one too.
func unsupported(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == 404 && ae.Code == codeNoPath
}

type client struct {
	base    string
	http    *http.Client
	dir     string
	creds   Credentials
	relogin bool // ensureLogin already asked once in this run
}

// apiTimeout is how long one request to aline.team may take.
var apiTimeout = 30 * time.Second

func newClient(dir string) (*client, error) {
	cfg, err := loadConfig(dir)
	if err != nil {
		return nil, err
	}
	base, err := apiBase(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	creds, err := credentialsFor(dir, base)
	if err != nil {
		return nil, err
	}
	return &client{base: base, http: &http.Client{Timeout: apiTimeout}, dir: dir, creds: creds}, nil
}

// credentialsFor loads the login for the aline.team API at base. A token issued by another server
// (say the dev server, by a build from source) is never sent here, so this build counts as logged out.
func credentialsFor(dir, base string) (Credentials, error) {
	c, err := loadCredentials(dir)
	if err != nil || c.Server == "" || c.Server == base {
		return c, err
	}
	return Credentials{}, nil
}

func (c *client) loggedIn() bool { return c.creds.Token != "" }

// call sends an authenticated request. A token the server no longer accepts (expired or revoked) is
// removed here. A person at the terminal logs in again right away with a code sent to the same email,
// and the request is sent again; hooks and scheduled runs cannot ask, so the next command does it.
func (c *client) call(method, path string, in, out any) error {
	err := c.send(method, path, in, out, true)
	var ae *apiError
	if !errors.As(err, &ae) || (ae.Code != codeTokenInvalid && ae.Code != codeNotLoggedIn) {
		return err
	}
	c.creds.Token, c.creds.TokenExpiresAt = "", ""
	if serr := saveCredentials(c.dir, c.creds); serr != nil {
		return serr
	}
	if c.ensureLogin() {
		return c.send(method, path, in, out, true)
	}
	return fmt.Errorf("%w %s", err, tr(detectLang(os.Getenv), "relogin"))
}

// ensureLogin makes sure this run is logged in. When the login is missing or expired, a person at the
// terminal goes straight into logging in: a code goes to the account's email right away when it is
// known; without one, or when that fails, the email is asked for. Once per run; hooks and scheduled
// runs cannot ask, and the next command at the terminal does it.
func (c *client) ensureLogin() bool {
	if c.loggedIn() {
		return true
	}
	if c.relogin || !interactive() {
		return false
	}
	c.relogin = true
	lang := detectLang(os.Getenv)
	if email := c.creds.Email; email != "" {
		warn(lang, "loginExpired", email)
		noSignup := func() (bool, bool) { return false, false } // the account exists; never make a new one here
		_, err := c.signIn(email, noSignup, askCode(lang, email, false))
		if err == nil {
			say(lang, "loggedIn", email)
			return true
		}
		if stdinClosed {
			return false
		}
		warn(lang, "loginOtherEmail", err)
	}
	if err := c.login(lang); err != nil {
		warn(lang, "failed", err)
		return false
	}
	return c.loggedIn()
}

// send makes one request and unwraps the server's {"success", "contents"} envelope into out.
// The token only ever goes into the Authorization header.
func (c *client) send(method, path string, in, out any, auth bool) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", detectLang(os.Getenv)) // texts aline.team writes, in the screen's language
	req.Header.Set("User-Agent", fmt.Sprintf("gitfolio/%s (%s; %s)", version, runtime.GOOS, runtime.GOARCH))
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.creds.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var env struct {
		Success  bool            `json:"success"`
		Contents json.RawMessage `json:"contents"`
		Error    *apiError       `json:"error"`
	}
	if jerr := json.Unmarshal(data, &env); jerr != nil || resp.StatusCode >= 300 || !env.Success {
		if env.Error == nil {
			env.Error = &apiError{}
		}
		if env.Error.Status == 0 {
			env.Error.Status = resp.StatusCode
		}
		return env.Error
	}
	if out != nil && len(env.Contents) > 0 && string(env.Contents) != "null" {
		return json.Unmarshal(env.Contents, out)
	}
	return nil
}

type signInResult struct {
	Token          string   `json:"token"`
	TokenExpiresAt string   `json:"tokenExpiresAt"`
	Account        struct { // the account ID is not read: the CLI never needs it
		Email   string `json:"email"`
		Created bool   `json:"created"`
	} `json:"account"`
	VerifiedEmails []string `json:"verifiedEmails"`
}

// deviceName tells devices apart in the aline.team token list and the code email. The host name is
// personal, so only the OS and CPU are sent. The server shows it in the email only in this exact
// "(macOS|Linux|Windows) <arch>" form, so it never carries free text.
func deviceName() string {
	name := map[string]string{"darwin": "macOS", "linux": "Linux", "windows": "Windows"}[runtime.GOOS]
	if name == "" {
		name = runtime.GOOS
	}
	return name + " " + runtime.GOARCH
}

// localTimezone returns this computer's IANA time zone ("Asia/Seoul"), or "" when it cannot tell;
// the server then uses UTC. Go's time.Local does not expose the name, so TZ and /etc/localtime are read.
func localTimezone() string {
	name := strings.TrimPrefix(os.Getenv("TZ"), ":")
	if name == "" {
		if link, err := os.Readlink("/etc/localtime"); err == nil {
			name = zoneFromPath(link)
		}
	}
	if name == "" || len(name) > 64 || name == "Local" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

// zoneFromPath takes the zone name out of a zoneinfo path such as /var/db/timezone/zoneinfo/Asia/Seoul.
func zoneFromPath(p string) string {
	if _, zone, ok := strings.Cut(p, "zoneinfo/"); ok {
		return zone
	}
	return ""
}

// maxCodeTries is how many codes the CLI asks for before it stops (A009 ends it earlier).
const maxCodeTries = 5

// errSignupCancelled is returned when the user does not want a new account for the email they typed.
var errSignupCancelled = errors.New("sign-up cancelled")

// signIn verifies the email with a one-time code and stores the token.
// When the email has no account yet, the server signs it up; confirmSignup is asked first, because
// signing up counts as agreeing to the terms and the privacy policy and a typo would make a stray
// account. It also returns whether the user wants aline.team service notifications (sign-up only).
// askCode gets the code length the server announced and is asked again while the code is wrong.
func (c *client) signIn(email string, confirmSignup func() (ok, notify bool), askCode func(retry bool, length int) string) (signInResult, error) {
	s, err := c.startSignIn(email)
	if err != nil {
		return signInResult{}, err
	}
	if !s.exists {
		ok, notify := confirmSignup()
		if !ok {
			return signInResult{}, errSignupCancelled
		}
		s.req["isNotified"] = notify // service notification opt-in, used for a new account only
	}
	for try := 0; ; try++ {
		res, err := c.finishSignIn(s, askCode(try > 0, s.length))
		if wrongCode(err) && try < maxCodeTries-1 {
			continue
		}
		return res, err
	}
}

// codeStart is a code sent by email and the request that will send it back: a sign-in or a work
// email check, finished by another call (gitfolio mcp asks for the code in a second tool call).
type codeStart struct {
	req    map[string]any // challengeId, and the device for a sign-in
	exists bool           // sign-in: the email has an account
	length int            // digits in the code
}

// codeEnded reports whether aline.team will take no more tries of a code: expired, or tried too often.
func codeEnded(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == codeExpired
}

func wrongCode(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == codeWrongCode
}

// startSignIn sends a sign-in code to email. The same device object goes to start (the code email
// shows the device and the time in its time zone, in its language for a new account) and to verify
// (language and time zone set up a new account).
func (c *client) startSignIn(email string) (codeStart, error) {
	device := map[string]string{"name": deviceName(), "language": strings.ToUpper(detectLang(os.Getenv))}
	if tz := localTimezone(); tz != "" {
		device["timezone"] = tz
	}
	var start struct {
		ChallengeID   string `json:"challengeId"`
		AccountExists bool   `json:"accountExists"`
		CodeLength    int    `json:"codeLength"`
	}
	if err := c.send("POST", "/cli/start", map[string]any{"email": email, "device": device}, &start, false); err != nil {
		return codeStart{}, err
	}
	return codeStart{map[string]any{"challengeId": start.ChallengeID, "device": device}, start.AccountExists, cmp.Or(start.CodeLength, 6)}, nil
}

// finishSignIn tries one code and, when it is right, stores the token.
func (c *client) finishSignIn(s codeStart, code string) (signInResult, error) {
	var res signInResult
	s.req["code"] = code
	if err := c.send("POST", "/cli/verify", s.req, &res, false); err != nil {
		return res, err
	}
	c.creds.Token, c.creds.TokenExpiresAt, c.creds.Email = res.Token, res.TokenExpiresAt, res.Account.Email
	c.creds.Server = c.base
	c.creds.Verified = lowered(res.VerifiedEmails)
	return res, saveCredentials(c.dir, c.creds)
}

// meResult is the body of GET /cli/me, also returned by the work email calls.
type meResult struct {
	Account struct {
		Email string `json:"email"`
	} `json:"account"`
	VerifiedEmails []string `json:"verifiedEmails"`
}

// me asks which account the token belongs to and keeps its verified emails.
func (c *client) me() (meResult, error) {
	var me meResult
	if err := c.call("GET", "/cli/me", nil, &me); err != nil {
		return me, err
	}
	return me, c.keepVerified(me.VerifiedEmails)
}

func (c *client) keepVerified(emails []string) error {
	c.creds.Verified = lowered(emails)
	return saveCredentials(c.dir, c.creds)
}

func lowered(emails []string) []string {
	out := make([]string, len(emails))
	for i, e := range emails {
		out[i] = strings.ToLower(strings.TrimSpace(e))
	}
	return out
}

// errSkipped is returned when the user leaves the code empty to verify the email later.
var errSkipped = errors.New("skipped")

// verifyEmail proves with a one-time code that the user owns a work email (POST /cli/emails, then
// /cli/emails/verify; DESIGN 6.1.2). askCode works as in signIn; an empty code skips the email (errSkipped).
func (c *client) verifyEmail(email string, askCode func(retry bool, length int) string) error {
	s, done, err := c.startEmailCheck(email)
	if err != nil || done {
		return err
	}
	for try := 0; ; try++ {
		code := askCode(try > 0, s.length)
		if code == "" {
			return errSkipped
		}
		err := c.finishEmailCheck(s, code)
		if wrongCode(err) && try < maxCodeTries-1 {
			continue
		}
		return err
	}
}

// startEmailCheck sends a code to a work email; done when it is already verified for the account.
func (c *client) startEmailCheck(email string) (s codeStart, done bool, err error) {
	device := map[string]string{"name": deviceName()}
	if tz := localTimezone(); tz != "" {
		device["timezone"] = tz
	}
	var start struct {
		ChallengeID string `json:"challengeId"`
		CodeLength  int    `json:"codeLength"`
	}
	err = c.call("POST", "/cli/emails", map[string]any{"email": email, "device": device}, &start)
	var ae *apiError
	switch {
	case errors.As(err, &ae) && ae.Code == codeEmailVerified:
		_, err = c.me()
		return s, true, err
	case errors.As(err, &ae) && ae.Code == codeTooManyEmails:
		return s, false, failure("emailTooMany")
	case err != nil:
		return s, false, err
	}
	return codeStart{req: map[string]any{"challengeId": start.ChallengeID}, length: cmp.Or(start.CodeLength, 6)}, false, nil
}

// finishEmailCheck tries one code for a work email and keeps the account's verified emails.
func (c *client) finishEmailCheck(s codeStart, code string) error {
	var me meResult
	err := c.call("POST", "/cli/emails/verify", map[string]any{"challengeId": s.req["challengeId"], "code": code}, &me)
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == codeTooManyEmails { // checked again here: codes asked for earlier
		return failure("emailTooMany")
	}
	if err != nil {
		return err
	}
	return c.keepVerified(me.VerifiedEmails)
}

// unverifyEmail removes a work email from the account's verified emails (DELETE /cli/emails).
// Repositories already merged because of it stay merged.
func (c *client) unverifyEmail(email string) error {
	var me meResult
	if err := c.call("DELETE", "/cli/emails?email="+url.QueryEscape(email), nil, &me); err != nil {
		return err
	}
	return c.keepVerified(me.VerifiedEmails)
}
