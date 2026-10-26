package main

import "testing"

func TestMask(t *testing.T) {
	m := newMasker([]string{"acme", "고객사"})
	for in, want := range map[string]string{
		"see https://jira.acme.com/browse/PAY-12 for details": "see [URL] for details",
		"contact jane.doe@acme.com":                           "contact [EMAIL]",
		"Co-Authored-By: Claude <noreply@anthropic.com>":      "Co-Authored-By: Claude <[EMAIL]>",
		"fix PAY-123 and #45":                                 "fix [TICKET] and [TICKET]",
		"support UTF-8 and SHA-256":                           "support UTF-8 and SHA-256",
		"server 10.0.0.12 down":                               "server [IP] down",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123":            "token [SECRET]",
		"key AKIAIOSFODNN7EXAMPLE":                            "key [SECRET]",
		"token aln_cli_token-of-the-fake-server-for-tests":    "token [SECRET]",
		"Acme 고객사 대응":                                         "[REDACTED] [REDACTED] 대응",
		"client_acme.go":                                      "client_[REDACTED].go",
		"internal-refactoring-of-the-module":                  "internal-refactoring-of-the-module",
		"plain message":                                       "plain message",
		"oops\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nq83vEi7=\n-----END RSA PRIVATE KEY-----\nsorry": "oops\n[SECRET]\nsorry",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nlQOYBF\n-----END PGP PRIVATE KEY BLOCK-----":                    "[SECRET]",
		"cut: -----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjE":                                           "cut: [SECRET]",
		"left by masking before: [SECRET]\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----":                       "left by masking before: [SECRET]",
		"rotate glpat-" + "AbCdEfGhIjKlMnOpQrSt and AIza" + "SyA1234567890abcdefghijklmnopqrstuv":                 "rotate [SECRET] and [SECRET]",
		"npm_" + "abcdefghijklmnopqrstuvwxyz0123456789 sk_" + "live_abcdefghijklmnopqrst1234":                     "[SECRET] [SECRET]",
	} {
		got := m.apply(in)
		if got != want {
			t.Errorf("apply(%q) = %q, want %q", in, got, want)
		}
		if again := m.apply(got); again != got {
			t.Errorf("apply is not idempotent for %q: %q -> %q", in, got, again)
		}
	}
}

// Fake token values are split in two (one constant still), so the source holds no token for secret
// scanners to block. Messages stored with older, weaker masking are masked again once, so the next sync sends them masked.
func TestStoredMaskedOnce(t *testing.T) {
	dir := t.TempDir()
	old := "fix\n[SECRET]\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\nglpat-" + "AbCdEfGhIjKlMnOpQrSt"
	if err := writeCommits(dir, []Commit{{Repo: "a", Hash: "a1", Message: old}}); err != nil {
		t.Fatal(err)
	}
	if err := scanRepos(dir, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	commits, _ := readCommits(dir)
	if cfg, _ := loadConfig(dir); len(commits) != 1 || commits[0].Message != "fix\n[SECRET]\n[SECRET]" || cfg.MaskVersion != maskVersion {
		t.Errorf("stored: %+v, mask version %d", commits, cfg.MaskVersion)
	}
}
