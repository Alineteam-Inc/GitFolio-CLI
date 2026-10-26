package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// The user's work emails (DESIGN 3.2): commits by any of them, or by a repository's git config
// user.email, count as the user's own. aline.team gets them all under one author email, the primary: the
// first in the list. From 0.2.0 aline.team takes a repository's commits only under an email verified for
// the account (the account email, or a work email verified with a code) or a noreply address, and merges a
// repository the user linked on the web under such an email with what GitFolio sends (DESIGN 6.1.2).
// A repository's git config user.email only tells which commits are the user's.

// primaryEmail is the author email sent for all of the user's commits: the first work email, else the
// global git config user.email.
func primaryEmail(cfg Config) string {
	if len(cfg.Emails) > 0 {
		return cfg.Emails[0]
	}
	out, _ := git(".", "config", "--global", "user.email")
	return strings.ToLower(strings.TrimSpace(out))
}

func validEmail(e string) bool {
	return strings.Count(e, "@") == 1 && !strings.HasPrefix(e, "@") && !strings.HasSuffix(e, "@") && !strings.ContainsAny(e, " \t")
}

// addEmails adds the valid, new emails to the list (lowercased) and returns the ones it could not take.
func addEmails(cfg *Config, emails []string) (bad []string) {
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e == "" {
			continue
		}
		if !validEmail(e) {
			bad = append(bad, e)
		} else if !slices.Contains(cfg.Emails, e) {
			cfg.Emails = append(cfg.Emails, e)
		}
	}
	return bad
}

// verified reports whether aline.team verified email for the logged-in account.
func verified(creds Credentials, email string) bool {
	return strings.EqualFold(creds.Email, email) || slices.Contains(creds.Verified, strings.ToLower(email))
}

// noreply reports whether email is a git service's address that hides the real one
// (id+login@users.noreply.github.com, users.noreply.gitlab.com or a GitLab server's). It cannot receive
// a code, and aline.team takes commits under it without one.
func noreply(email string) bool {
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	return strings.HasPrefix(domain, "users.noreply.")
}

// emailList shows the work emails, the primary marked with ★ and, when logged in, the verified ones with ✔.
func emailList(lang string, cfg Config, creds Credentials) string {
	if len(cfg.Emails) == 0 {
		return tr(lang, "emailNone")
	}
	s, unverified := tr(lang, "emailList"), false
	for i, e := range cfg.Emails {
		s += map[bool]string{true: "★ ", false: "  "}[i == 0] + e
		switch {
		case noreply(e):
			s += " (noreply)"
		case creds.Token != "" && verified(creds, e):
			s += " " + symDone
		case creds.Token != "":
			unverified = true
		}
		s += "\n"
	}
	if unverified {
		s += tr(lang, "emailUnverified")
	}
	return s
}

// verifyEmails verifies, one after the other, each email not verified yet with a code sent to it.
// Enter skips one; a failure is reported and the next email goes on. The emails stay in the list either way.
func verifyEmails(lang string, c *client, emails []string) {
	for _, e := range emails {
		if verified(c.creds, e) || noreply(e) {
			continue
		}
		switch err := c.verifyEmail(e, askCode(lang, e, true)); {
		case errors.Is(err, errSkipped):
			say(lang, "emailSkipped", e)
		case err != nil:
			warn(lang, "emailVerifyFailed", e, err)
		default:
			say(lang, "emailVerified", e)
		}
	}
}

// cmdEmail shows or changes the work emails: add, rm, primary.
func cmdEmail(dir string, args []string) error {
	jsonMode(args)
	args = withoutJSON(args)
	lang := detectLang(os.Getenv)
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	if c.loggedIn() {
		_, _ = c.me() // fresh ✔ marks; offline, the last known ones
	}
	if len(args) == 0 {
		if jsonOut {
			return emailsJSON(cfg, c.creds)
		}
		show(out, emailList(lang, cfg, c.creds))
		return nil
	}
	if len(args) < 2 || !slices.Contains([]string{"add", "verify", "rm", "primary"}, args[0]) ||
		(args[0] == "verify" && len(args) != 2 && (len(args) != 4 || args[2] != "--code")) {
		return wrongWord("email", args[0], []string{"add", "verify", "rm", "primary"},
			"gitfolio email [add <email>... | verify <email> [--code <code>] | rm <email> | primary <email>] [--json]")
	}
	e := strings.ToLower(strings.TrimSpace(args[1]))
	var done string
	switch args[0] {
	case "add":
		if bad := addEmails(&cfg, args[1:]); len(bad) > 0 {
			return failure("emailBad", strings.Join(bad, ", "))
		}
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		if c.loggedIn() && interactive() {
			verifyEmails(lang, c, lowered(args[1:]))
		}
		done = tr(lang, "emailAdded")
	case "verify":
		if !validEmail(e) {
			return failure("emailBad", e)
		}
		if noreply(e) {
			return failure("emailNoreply", e)
		}
		if !c.ensureLogin() {
			return failure("loginFirst")
		}
		addEmails(&cfg, []string{e})
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		if len(args) == 2 && !interactive() { // a program, in two runs: the code now, --code finishes
			s, already, err := c.startEmailCheck(e)
			if err != nil {
				return err
			}
			if !already {
				if err := keepCode(dir, "email", e, &s); err != nil {
					return err
				}
				if jsonOut {
					return printJSON(map[string]any{"email": e, "verified": false, "codeLength": s.length})
				}
				say(lang, "emailCodeSent", e, e)
				return nil
			}
		} else if len(args) == 4 {
			if err := finishEmailCode(dir, c, e, args[3]); err != nil {
				return err
			}
		} else if err := c.verifyEmail(e, askCode(lang, e, true)); errors.Is(err, errSkipped) {
			done = fmt.Sprintf(tr(lang, "emailSkipped"), e)
			break
		} else if err != nil {
			return err
		}
		done = fmt.Sprintf(tr(lang, "emailVerified"), e)
	case "rm":
		i := slices.Index(cfg.Emails, e)
		switch {
		case i < 0:
			return failure("emailNotFound", e)
		case i == 0:
			return failure("emailRmPrimary", e) // pick another primary first, so there always is one
		}
		cfg.Emails = slices.Delete(cfg.Emails, i, i+1)
		if c.loggedIn() && slices.Contains(c.creds.Verified, e) {
			if err := c.unverifyEmail(e); err != nil {
				warn(lang, "emailUnverifyFailed", e, err) // removed here anyway; aline.team keeps it verified
			}
		}
		done = tr(lang, "emailRemoved")
	case "primary":
		if !validEmail(e) {
			return failure("emailBad", e)
		}
		cfg.Emails = append([]string{e}, slices.DeleteFunc(cfg.Emails, func(x string) bool { return x == e })...)
		done = fmt.Sprintf(tr(lang, "emailPrimary"), e)
	}
	if err := saveConfig(dir, cfg); err != nil {
		return err
	}
	if jsonOut {
		return emailsJSON(cfg, c.creds)
	}
	show(out, done+emailList(lang, cfg, c.creds))
	return nil
}

// finishEmailCode tries the code sent to work email e by an earlier run (email verify without --code).
func finishEmailCode(dir string, c *client, e, code string) error {
	return finishKept(dir, "email", e, code, func(s codeStart) error { return c.finishEmailCheck(s, code) })
}

// emailsJSON is the work emails for a program: the primary first, each verified or not.
func emailsJSON(cfg Config, creds Credentials) error {
	list := []map[string]any{}
	for i, e := range cfg.Emails {
		list = append(list, map[string]any{"email": e, "primary": i == 0, "verified": verified(creds, e), "noreply": noreply(e)})
	}
	return printJSON(map[string]any{"emails": list})
}

// checkEmails asks, once, a user set up before 0.2.0 to verify the work emails: aline.team now takes a
// repository's commits only under a verified email. init asks this itself; hooks and pipes never ask.
func checkEmails(dir, lang string) {
	cfg, err := loadConfig(dir)
	if err != nil || cfg.EmailsChecked || len(cfg.Emails) == 0 || !interactive() || !terminal(os.Stdout) {
		return
	}
	c, err := newClient(dir)
	if err != nil || !c.loggedIn() {
		return
	}
	if _, err := c.me(); err != nil {
		return // offline: next time
	}
	cfg.EmailsChecked = true
	if saveConfig(dir, cfg) != nil {
		return
	}
	unverified := slices.DeleteFunc(slices.Clone(cfg.Emails), func(e string) bool { return verified(c.creds, e) || noreply(e) })
	if len(unverified) == 0 {
		return
	}
	notice(fmt.Sprintf(tr(lang, "emailCheckOnce"), strings.Join(unverified, ", ")))
	if askYesNo(lang, "emailVerifyNow", true) && !stdinClosed {
		verifyEmails(lang, c, unverified)
	}
	blank()
}
