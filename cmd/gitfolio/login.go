package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const loginUsage = "gitfolio login [--email <email> [--code <code> [--signup [--notify]]]] [--json]"

// cmdLogin signs in to aline.team with an email code (DESIGN 6.1): asking at the terminal (login), or
// in two runs a program makes without asking anything: --email sends the code, and --email with --code
// finishes. An email without an account is signed up only with --signup, after the program showed the
// notice the first run gives.
func cmdLogin(dir string, args []string) error {
	jsonMode(args)
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	email := fs.String("email", "", "the account's email")
	code := fs.String("code", "", "the code emailed by the first run")
	signup := fs.Bool("signup", false, "agree to sign up when the email has no account")
	notify := fs.Bool("notify", false, "with --signup: receive aline.team service notifications by email")
	fs.Bool("json", false, "print JSON")
	if fs.Parse(args) != nil || fs.NArg() > 0 || (*email == "" && (*code != "" || *signup || *notify)) {
		return failure("usage", loginUsage)
	}
	lang := detectLang(os.Getenv)
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	if c.loggedIn() {
		if *email != "" {
			return failure("alreadyLoggedIn", c.creds.Email)
		}
		say(lang, "alreadyLoggedIn", c.creds.Email)
		return nil
	}
	if *email == "" {
		if !interactive() {
			return failure("loginNeedsTerminal")
		}
		return c.login(lang)
	}
	if !validEmail(*email) {
		return failure("emailInvalid")
	}
	terms := fmt.Sprintf(tr(lang, "signupNotice"), *email)
	if *code == "" {
		s, err := c.startSignIn(*email)
		if err != nil {
			return err
		}
		if err := keepCode(dir, "login", *email, &s); err != nil {
			return err
		}
		if jsonOut {
			res := map[string]any{"email": *email, "accountExists": s.exists, "codeLength": s.length}
			if !s.exists { // for the program to show before it signs up, in the screen's language
				res["signupNotice"], res["notifyQuestion"] = terms, tr(lang, "notifyAsk")
			}
			return printJSON(res)
		}
		say(lang, "codeSent", *email, *email)
		if !s.exists {
			notice(terms + tr(lang, "signupFlags"))
		}
		return nil
	}
	s, ok, err := keptCode(dir, "login", *email)
	if err != nil {
		return err
	}
	if !ok {
		return failure("codeNotSent", *email)
	}
	if !validCode(*code, s.length) {
		return failure("codeFormat", s.length)
	}
	if !s.exists {
		if !*signup { // what the first run shows, for a code an AI tool sent (MCP login_start)
			if !jsonOut {
				notice(terms + tr(lang, "signupFlags"))
			}
			err := failure("signupConsent", *email).(*failErr)
			err.extra = map[string]string{"signupNotice": terms, "notifyQuestion": tr(lang, "notifyAsk")}
			return err
		}
		s.req["isNotified"] = *notify // service notification opt-in, used for a new account only
	}
	res, err := c.finishSignIn(s, *code)
	if err == nil || codeEnded(err) { // a wrong code keeps it: aline.team takes a few tries
		if kerr := keepCode(dir, "login", *email, nil); err == nil {
			err = kerr
		}
	}
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(map[string]any{"email": res.Account.Email, "created": res.Account.Created, "verifiedEmails": res.VerifiedEmails})
	}
	if res.Account.Created {
		show(out, fmt.Sprintf(tr(lang, "signedUp"), res.Account.Email)+tr(lang, "passwordMail"))
	} else {
		say(lang, "loggedIn", res.Account.Email)
	}
	return nil
}

// codeFile keeps the codes sent by email that a later run finishes (login --code, email verify --code),
// one per sign-in or work email, until the code is right or runs out.
const codeFile = "codes.json"

// keptCodeStart is a codeStart as codeFile keeps it.
type keptCodeStart struct {
	Req    map[string]any `json:"req"`
	Exists bool           `json:"exists,omitempty"`
	Length int            `json:"length"`
}

// keepCode keeps s for a later run to finish, or forgets the code waiting for kind and email when s is nil.
func keepCode(dir, kind, email string, s *codeStart) error {
	p, key := filepath.Join(dir, codeFile), kind+" "+strings.ToLower(email)
	codes := map[string]keptCodeStart{}
	if err := loadJSON(p, &codes); err != nil {
		return err
	}
	if s == nil {
		delete(codes, key)
	} else {
		codes[key] = keptCodeStart{s.req, s.exists, s.length}
	}
	return saveJSON(p, codes)
}

// keptCode is the code waiting for kind ("login") and email, if any.
func keptCode(dir, kind, email string) (codeStart, bool, error) {
	var codes map[string]keptCodeStart
	if err := loadJSON(filepath.Join(dir, codeFile), &codes); err != nil {
		return codeStart{}, false, err
	}
	k, ok := codes[kind+" "+strings.ToLower(email)]
	return codeStart{k.Req, k.Exists, k.Length}, ok, nil
}

// login asks for an email and signs in with a code sent to it; an email without an account is signed
// up after showing the terms, the privacy policy and the email it will be made for.
func (c *client) login(lang string) error {
	section(lang, "loginTitle")
	email := ""
	for {
		email = strings.TrimSpace(prompt(tr(lang, "emailAsk")))
		if stdinClosed {
			return failure("emailInvalid")
		}
		// Catch typos (e.g. a leftover IME character and a space) here, before the server
		// rejects the request and the whole login has to start over.
		if validEmail(email) {
			break
		}
		notice(tr(lang, "emailInvalid"))
	}
	confirmSignup := func() (ok, notify bool) {
		notice(fmt.Sprintf(tr(lang, "signupNotice"), email))
		if !askYesNo(lang, "signupAsk", true) || stdinClosed { // no input left is no account
			return false, false
		}
		return true, askYesNo(lang, "notifyAsk", false) // opt-in: only an explicit yes
	}
	res, err := c.signIn(email, confirmSignup, askCode(lang, email, false))
	if errors.Is(err, errSignupCancelled) {
		say(lang, "signupCancelled")
		return nil
	}
	if err != nil {
		return err
	}
	if res.Account.Created {
		show(out, fmt.Sprintf(tr(lang, "signedUp"), res.Account.Email)+tr(lang, "passwordMail"))
	} else {
		say(lang, "loggedIn", res.Account.Email)
	}
	return nil
}

// askCode asks for the code sent to email, again while it is not the announced number of digits.
// When skippable, Enter alone returns "" (a work email can be verified later).
func askCode(lang, email string, skippable bool) func(retry bool, length int) string {
	ask := map[bool]string{false: "codeAsk", true: "codeAskSkip"}[skippable]
	return func(retry bool, length int) string {
		if retry {
			notice(tr(lang, "codeWrong"))
		}
		for {
			code := strings.TrimSpace(prompt(fmt.Sprintf(tr(lang, ask), email)))
			// A typo is caught here, so it neither ends the login nor uses up one of the server's tries.
			if stdinClosed || validCode(code, length) || (skippable && code == "") {
				return code
			}
			notice(fmt.Sprintf(tr(lang, "codeFormat"), length))
		}
	}
}

// validCode reports whether code is exactly length digits.
func validCode(code string, length int) bool {
	return len(code) == length && strings.Trim(code, "0123456789") == ""
}

// cmdWhoami asks aline.team which account this device's token belongs to (GET /cli/me).
func cmdWhoami(dir string, args []string) error {
	jsonMode(args)
	if len(args) > 0 && !slices.Equal(args, []string{"--json"}) {
		return failure("usage", "gitfolio whoami [--json]")
	}
	lang := detectLang(os.Getenv)
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	if !c.ensureLogin() {
		if jsonOut {
			return printJSON(map[string]any{"loggedIn": false})
		}
		say(lang, "notLoggedIn")
		return nil
	}
	me, err := c.me()
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(map[string]any{"loggedIn": true, "email": me.Account.Email, "verifiedEmails": me.VerifiedEmails})
	}
	sayKV(lang, "whoami", me.Account.Email, strings.Join(me.VerifiedEmails, ", "), c.base)
	return nil
}

// cmdLogout revokes this device's token on aline.team (best effort) and deletes it here.
func cmdLogout(dir string, args []string) error {
	jsonMode(args)
	if len(args) > 0 && !slices.Equal(args, []string{"--json"}) {
		return failure("usage", "gitfolio logout [--json]")
	}
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	revoked := false
	if c.loggedIn() {
		if err := c.call("POST", "/cli/logout", nil, nil); err != nil {
			warn(detectLang(os.Getenv), "logoutServerFailed", err)
		} else {
			revoked = true
		}
	}
	if err := saveCredentials(dir, Credentials{}); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(map[string]any{"revoked": revoked})
	}
	say(detectLang(os.Getenv), "loggedOut")
	return nil
}
