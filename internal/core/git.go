package core

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GitAuthMode enumerates how qui-sync authenticates to a git remote.
type GitAuthMode string

const (
	GitAuthPublic       GitAuthMode = "public"         // HTTPS, no auth
	GitAuthSSHDeployKey GitAuthMode = "ssh_deploy_key" // SSH with dedicated keypair
	GitAuthToken        GitAuthMode = "token"          // Fine-grained PAT injected via HTTPS
)

// GitAuth carries the credentials for a single subscription.
// For ssh_deploy_key: KeyFile points at the private key on disk.
// For token: Token is the PAT string (kept in memory only; on disk
// it lives in /config/git-keys/<slug>/token with 0600 perms).
type GitAuth struct {
	Mode    GitAuthMode
	KeyFile string
	Token   string
}

// perRepoLocks serializes git operations per clone directory so two
// concurrent "pull" or "clone" calls on the same subscription don't
// trample each other. Different subscriptions run in parallel.
var (
	gitLocksMu sync.Mutex
	gitLocks   = map[string]*sync.Mutex{}
)

func gitLockFor(dir string) *sync.Mutex {
	gitLocksMu.Lock()
	defer gitLocksMu.Unlock()
	m, ok := gitLocks[dir]
	if !ok {
		m = &sync.Mutex{}
		gitLocks[dir] = m
	}
	return m
}

// ValidateGitURL rejects URLs that fail a minimum sanity bar before
// the value reaches `git`. Catches three classes of attack at a single
// chokepoint:
//
//   - file:// or other non-network schemes that would let an admin
//     read on-disk repos via the probe endpoint or subscription add
//   - leading "-" which `git` would otherwise consume as a flag
//     (fall-through belt to the "--" separator the call sites add)
//   - misc bogus schemes that aren't actual transports
//
// Accepts: https, http, ssh, git, plus the SCP-style user@host:path
// form (no scheme but everyone uses it for GitHub SSH). Reject anything
// else loud and clear so a misconfigured URL gives a usable error.
func ValidateGitURL(rawURL string) error {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return fmt.Errorf("url is required")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("url cannot start with '-'")
	}
	// SCP-form SSH (git@host:path) — no scheme, host has user@ prefix
	// and a ':' before any '/'. Common for GitHub SSH urls.
	if !strings.Contains(s, "://") {
		at := strings.Index(s, "@")
		if at > 0 {
			rest := s[at+1:]
			if i := strings.Index(rest, ":"); i > 0 && i < strings.IndexAny(rest+"/", "/") {
				return nil
			}
		}
		return fmt.Errorf("url must use https://, ssh://, or git@host:path form")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http", "ssh", "git":
		return nil
	default:
		return fmt.Errorf("unsupported url scheme %q (allowed: https, http, ssh, git, or git@host:path)", u.Scheme)
	}
}

// SanitizeURLForDisplay strips userinfo (user:pass@) so a token-in-URL
// doesn't leak through error messages or toasts. Lossy by design;
// callers that need the original URL should keep their own copy.
func SanitizeURLForDisplay(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

// CloneSubscription clones a remote git repo into destDir at the given
// branch. The directory must not already contain a git repo; use
// FetchSubscription to update an existing clone.
func CloneSubscription(ctx context.Context, remoteURL, branch, destDir string, auth GitAuth) error {
	lock := gitLockFor(destDir)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(destDir, ".git")); err == nil {
		return fmt.Errorf("%s already contains a git repo — use FetchSubscription instead", destDir)
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return err
	}

	effectiveURL, env, err := prepareAuth(remoteURL, auth)
	if err != nil {
		return err
	}

	args := []string{"clone", "--depth", "1", "--single-branch"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	// "--" separates options from positional args so a URL beginning
	// with '-' (or a URL crafted to look like one) can't be consumed
	// as a flag. Defense in depth — ValidateGitURL rejects leading '-'
	// at the API boundary, but the separator removes the entire class
	// of regression even if a future code path bypasses validation.
	args = append(args, "--", effectiveURL, destDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone: %w — %s", err, sanitizeGitOutput(string(out), auth))
	}
	return nil
}

// FetchSubscription runs `git fetch` then hard-resets the working tree to
// origin/<branch>. Returns the new HEAD SHA on success. Intended for
// non-interactive updates where we want to match upstream exactly —
// the subscription clone is read-only from qui-sync's perspective.
func FetchSubscription(ctx context.Context, destDir, branch string, auth GitAuth) (string, error) {
	lock := gitLockFor(destDir)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(destDir, ".git")); err != nil {
		return "", fmt.Errorf("%s is not a git repo — call CloneSubscription first", destDir)
	}

	// Re-compute auth env in case the stored URL needs token injection.
	// We read origin's URL, rebuild with auth (for HTTPS+token only), and
	// pass it via `git -c` to avoid rewriting .git/config.
	originURL, err := runGit(ctx, destDir, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", fmt.Errorf("read origin url: %w", err)
	}
	originURL = strings.TrimSpace(originURL)

	effectiveURL, env, err := prepareAuth(originURL, auth)
	if err != nil {
		return "", err
	}

	br := branch
	if br == "" {
		br = "HEAD"
	}
	// Fetch via the URL with embedded credentials. "-c remote.origin.url=X"
	// has been observed to send anonymous requests in some git versions —
	// passing the URL positionally is the reliable form and leaves
	// .git/config untouched.
	if _, err := runGit(ctx, destDir, env,
		"fetch", "--depth", "1", "--", effectiveURL, br); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, destDir, env, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return CurrentSHA(ctx, destDir)
}

// CurrentSHA returns the short SHA of the checked-out HEAD.
func CurrentSHA(ctx context.Context, destDir string) (string, error) {
	out, err := runGit(ctx, destDir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GitCommitExport stages all changes in the share-repo and creates a
// commit with an auto-generated message. Called after RunExport to make
// the export immediately available for Sync/Pull. Idempotent — if there
// are no staged changes, git commit exits cleanly and we return nil.
func GitCommitExport(ctx context.Context, repoDir string) error {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoDir)
	}
	// Configure committer identity if not already set.
	_ = runGitSilent(ctx, repoDir, nil, "config", "user.name", "qui-sync")
	_ = runGitSilent(ctx, repoDir, nil, "config", "user.email", "qui-sync@local")

	if _, err := runGit(ctx, repoDir, nil, "add", "-A"); err != nil {
		return err
	}
	// --allow-empty is not used — if nothing staged, commit returns 1
	// which we treat as "nothing to commit" (success).
	_, err := runGit(ctx, repoDir, nil, "commit", "-m", "qui-sync export")
	if err != nil && strings.Contains(err.Error(), "nothing to commit") {
		return nil
	}
	return err
}

// runGitSilent is like runGit but discards errors (used for optional config).
func runGitSilent(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := runGit(ctx, dir, env, args...)
	return err
}

// GitCommitNoteUpdate stages CHANGELOG.md and creates a follow-up
// commit for an edited export note. "nothing to commit" is treated as
// success — the UI may trigger this with no actual text change.
func GitCommitNoteUpdate(ctx context.Context, repoDir string) error {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoDir)
	}
	_ = runGitSilent(ctx, repoDir, nil, "config", "user.name", "qui-sync")
	_ = runGitSilent(ctx, repoDir, nil, "config", "user.email", "qui-sync@local")
	if _, err := runGit(ctx, repoDir, nil, "add", "CHANGELOG.md"); err != nil {
		return err
	}
	_, err := runGit(ctx, repoDir, nil, "commit", "-m", "qui-sync: update export note")
	if err != nil && strings.Contains(err.Error(), "nothing to commit") {
		return nil
	}
	return err
}

// SetupGitRepo initializes a git repo at repoDir (if not already) and
// sets the origin remote URL. Idempotent — safe to call multiple times.
func SetupGitRepo(ctx context.Context, repoDir, remoteURL string) error {
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return err
	}

	// Init if not already a git repo.
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		if _, err := runGit(ctx, repoDir, nil, "init"); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
		_ = runGitSilent(ctx, repoDir, nil, "config", "user.name", "qui-sync")
		_ = runGitSilent(ctx, repoDir, nil, "config", "user.email", "qui-sync@local")
		_ = runGitSilent(ctx, repoDir, nil, "checkout", "-b", "main")
	}

	// Set or update origin.
	existing, err := runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err != nil || strings.TrimSpace(existing) == "" {
		// No origin yet — add it.
		_, err = runGit(ctx, repoDir, nil, "remote", "add", "origin", remoteURL)
	} else {
		// Origin exists — update URL.
		_, err = runGit(ctx, repoDir, nil, "remote", "set-url", "origin", remoteURL)
	}
	if err != nil {
		return fmt.Errorf("set remote: %w", err)
	}

	// Create initial .gitignore if missing.
	giPath := filepath.Join(repoDir, ".gitignore")
	if _, err := os.Stat(giPath); os.IsNotExist(err) {
		_ = os.WriteFile(giPath, []byte(""), 0o644)
	}

	return nil
}

// RemoveGitRemote removes the origin remote from a repo, leaving the
// working tree and history intact. Idempotent — a repo with no
// origin (or no .git at all) returns nil. Used by the "stop
// publishing to GitHub" UI affordance, where the user wants to
// disconnect without deleting their local exports.
func RemoveGitRemote(ctx context.Context, repoDir string) error {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		// No git repo, nothing to remove. Treat as success so the
		// caller doesn't have to special-case "never configured".
		return nil
	}
	// `git remote remove origin` errors if origin doesn't exist; use
	// runGitSilent so the caller gets idempotent behaviour without
	// special-casing the error string.
	_ = runGitSilent(ctx, repoDir, nil, "remote", "remove", "origin")
	return nil
}

// GetGitRemote returns the origin URL of the repo, or error if not configured.
func GetGitRemote(ctx context.Context, repoDir string) (string, error) {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return "", fmt.Errorf("not a git repo")
	}
	out, err := runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RepoStatus returns the sync state of a git repo against its remote.
type RepoStatus struct {
	Clean        bool   `json:"clean"`          // no uncommitted changes
	Ahead        int    `json:"ahead"`          // commits ahead of origin
	Behind       int    `json:"behind"`         // commits behind origin
	LastCommit   string `json:"last_commit"`    // short message of HEAD
	LastCommitAt string `json:"last_commit_at"` // date of HEAD
	RemoteURL    string `json:"remote_url"`     // origin URL (token stripped)
	Branch       string `json:"branch"`         // current branch
}

// GetRepoStatus checks the git repo's state relative to origin.
//
// token is optional. If provided it is injected into the origin URL for
// the fetch that populates refs/remotes/origin/<branch>; without it the
// fetch falls back to anonymous and silently no-ops on private repos —
// which used to leave ahead/behind stuck at 0 and the UI showing a
// misleading "In sync with remote" even when the user had unpushed
// commits. The fetch always uses an explicit refspec
// (<branch>:refs/remotes/origin/<branch>) so the tracking ref exists
// even when qui-sync's other write paths (PullRepo / ResetLocalToRemote)
// have only ever populated FETCH_HEAD.
func GetRepoStatus(ctx context.Context, repoDir, token string) (*RepoStatus, error) {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return &RepoStatus{}, nil // not a git repo yet
	}

	status := &RepoStatus{}

	// Clean?
	out, err := runGit(ctx, repoDir, nil, "status", "--porcelain")
	if err == nil {
		status.Clean = strings.TrimSpace(out) == ""
	}

	// Branch
	out, err = runGit(ctx, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		status.Branch = strings.TrimSpace(out)
	}

	// Fetch to populate refs/remotes/origin/<branch> so the rev-list
	// counts below have something to compare against. Best-effort —
	// network/auth failures leave ahead/behind at zero rather than
	// breaking the status endpoint entirely.
	if status.Branch != "" {
		if originOut, err := runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url"); err == nil {
			originURL := strings.TrimSpace(originOut)
			if originURL != "" {
				auth := GitAuth{Mode: GitAuthPublic}
				if token != "" {
					auth = GitAuth{Mode: GitAuthToken, Token: token}
				}
				if effectiveURL, env, err := prepareAuth(originURL, auth); err == nil {
					// Force-update with `+` so a remote rebase/force-push is reflected.
					refspec := "+" + status.Branch + ":refs/remotes/origin/" + status.Branch
					_ = runGitSilent(ctx, repoDir, env, "fetch", "--", effectiveURL, refspec)
				}
			}
		}
	}

	// Ahead
	out, err = runGit(ctx, repoDir, nil, "rev-list", "--count", "origin/"+status.Branch+"..HEAD")
	if err == nil {
		fmt.Sscanf(strings.TrimSpace(out), "%d", &status.Ahead)
	}

	// Behind
	out, err = runGit(ctx, repoDir, nil, "rev-list", "--count", "HEAD..origin/"+status.Branch)
	if err == nil {
		fmt.Sscanf(strings.TrimSpace(out), "%d", &status.Behind)
	}

	// Last commit
	out, err = runGit(ctx, repoDir, nil, "log", "-1", "--format=%s")
	if err == nil {
		status.LastCommit = strings.TrimSpace(out)
	}
	out, err = runGit(ctx, repoDir, nil, "log", "-1", "--format=%ci")
	if err == nil {
		status.LastCommitAt = strings.TrimSpace(out)
	}

	// Remote URL — strip any embedded credentials before exposing to UI.
	// We don't write user@host into the stored remote anymore (prepareAuth
	// builds a per-call URL with credentials), but a hand-edited
	// .git/config or a legacy install could still have one.
	out, err = runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err == nil {
		status.RemoteURL = SanitizeURLForDisplay(strings.TrimSpace(out))
	}

	return status, nil
}

// PullRepo pulls latest from origin for the current branch. Uses PAT for auth.
func PullRepo(ctx context.Context, repoDir, token string) error {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoDir)
	}
	originURL, err := runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("read origin url: %w", err)
	}
	originURL = strings.TrimSpace(originURL)

	auth := GitAuth{Mode: GitAuthToken, Token: token}
	effectiveURL, env, err := prepareAuth(originURL, auth)
	if err != nil {
		return err
	}

	branch, err := runGit(ctx, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	branch = strings.TrimSpace(branch)

	// Fetch from the URL that has the PAT embedded, then fast-forward
	// merge locally. Pushing/pulling via "-c remote.origin.url=X" has
	// been observed to send anonymous requests in some git versions
	// (the credential override doesn't always propagate to the HTTPS
	// backend) — fetching via an explicit URL bypasses that path.
	//
	// Refspec writes refs/remotes/origin/<branch> in addition to
	// FETCH_HEAD so subsequent GetRepoStatus calls have a tracking ref
	// to compute ahead/behind against.
	refspec := "+" + branch + ":refs/remotes/origin/" + branch
	if _, err := runGit(ctx, repoDir, env, "fetch", "--", effectiveURL, refspec); err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	if _, err := runGit(ctx, repoDir, nil, "merge", "--ff-only", "refs/remotes/origin/"+branch); err != nil {
		return fmt.Errorf("pull (merge): %w", err)
	}
	return nil
}

// ResetLocalToRemote fetches origin/<current-branch> and hard-resets the
// local working tree and branch pointer to match it. Used when the local
// repo's history is divergent from the remote (typically a first-time
// setup where qui-sync ran `git init` against an existing remote — the
// two histories are "unrelated" and git push / pull refuse to reconcile).
//
// This operation is DESTRUCTIVE: any local commits that are not already
// on the remote are thrown away. Callers must confirm with the user
// before invoking.
//
// Token is optional — an empty token fetches anonymously, which works
// for public repos. If the repo is private the token is required.
func ResetLocalToRemote(ctx context.Context, repoDir, token string) error {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoDir)
	}
	originURL, err := runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("read origin url: %w", err)
	}
	originURL = strings.TrimSpace(originURL)
	if originURL == "" {
		return fmt.Errorf("no origin remote configured")
	}

	auth := GitAuth{Mode: GitAuthPublic}
	if token != "" {
		auth = GitAuth{Mode: GitAuthToken, Token: token}
	}
	effectiveURL, env, err := prepareAuth(originURL, auth)
	if err != nil {
		return err
	}

	branch, err := runGit(ctx, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("determine branch: %w", err)
	}
	branch = strings.TrimSpace(branch)

	// Refspec populates refs/remotes/origin/<branch> in addition to
	// FETCH_HEAD so the post-reset GetRepoStatus has a tracking ref
	// for ahead/behind comparisons.
	refspec := "+" + branch + ":refs/remotes/origin/" + branch
	if _, err := runGit(ctx, repoDir, env, "fetch", "--", effectiveURL, refspec); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if _, err := runGit(ctx, repoDir, nil, "reset", "--hard", "refs/remotes/origin/"+branch); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	return nil
}

// RepoDiffSummary returns a human-readable summary of what would be pushed.
func RepoDiffSummary(ctx context.Context, repoDir string) (string, error) {
	branch, err := runGit(ctx, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch = strings.TrimSpace(branch)
	out, err := runGit(ctx, repoDir, nil, "diff", "--stat", "origin/"+branch+"..HEAD")
	if err != nil {
		return "(could not compute diff)", nil
	}
	return strings.TrimSpace(out), nil
}

// PushRepo pushes the current branch to origin using a PAT for auth.
func PushRepo(ctx context.Context, repoDir, token string) error {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoDir)
	}

	// Read the remote URL + inject token for this push only
	originURL, err := runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("read origin url: %w", err)
	}
	originURL = strings.TrimSpace(originURL)

	auth := GitAuth{Mode: GitAuthToken, Token: token}
	effectiveURL, env, err := prepareAuth(originURL, auth)
	if err != nil {
		return err
	}

	branch, err := runGit(ctx, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	branch = strings.TrimSpace(branch)

	// Push directly to the URL with embedded PAT, bypassing the remote
	// config entirely. "-c remote.origin.url=X" has been observed to
	// send anonymous requests in some git versions — the override does
	// not always propagate to the HTTPS credential resolution. Passing
	// the URL as a positional argument to git push is the reliable form.
	_, err = runGit(ctx, repoDir, env, "push", "--", effectiveURL, branch)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// ---- internals ----

// prepareAuth rewrites the URL (for token mode) and builds env overrides
// (for SSH mode) so that a single `git` invocation can use the caller's
// credentials without touching .git/config.
func prepareAuth(remoteURL string, auth GitAuth) (effectiveURL string, env []string, err error) {
	switch auth.Mode {
	case "", GitAuthPublic:
		return remoteURL, nil, nil

	case GitAuthSSHDeployKey:
		if auth.KeyFile == "" {
			return "", nil, fmt.Errorf("ssh_deploy_key auth requires KeyFile")
		}
		if _, err := os.Stat(auth.KeyFile); err != nil {
			return "", nil, fmt.Errorf("ssh key file: %w", err)
		}
		// -o StrictHostKeyChecking=no trades off first-run MITM protection
		// for not requiring users to manage known_hosts. Acceptable for
		// deploy-key access to public Git hosts.
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o IdentitiesOnly=yes",
			shellQuote(auth.KeyFile))
		return remoteURL, []string{"GIT_SSH_COMMAND=" + sshCmd}, nil

	case GitAuthToken:
		if auth.Token == "" {
			return "", nil, fmt.Errorf("token auth requires Token")
		}
		u, err := url.Parse(remoteURL)
		if err != nil {
			return "", nil, fmt.Errorf("parse url: %w", err)
		}
		if u.Scheme != "https" {
			return "", nil, fmt.Errorf("token auth requires https URL (got %s)", u.Scheme)
		}
		// GitHub's fine-grained PAT format: https://<token>@github.com/...
		// This keeps the token out of the clone's stored remote by using
		// -c remote.origin.url= at call time (see FetchSubscription).
		u.User = url.UserPassword("x-access-token", auth.Token)
		return u.String(), nil, nil
	}
	return "", nil, fmt.Errorf("unknown git auth mode %q", auth.Mode)
}

func runGit(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// GIT_TERMINAL_PROMPT=0 makes git fail fast when credentials are needed
	// instead of trying to read interactively from a TTY that does not exist
	// in the container — the cryptic "could not read Username ... No such
	// device or address" error becomes the clean "terminal prompts disabled".
	//
	// GIT_ASKPASS=/bin/false signals "no external credential helper
	// available" with a non-zero exit. We deliberately do NOT use
	// /bin/true here — /bin/true exits 0 with empty stdout, which git
	// interprets as "no credentials" and quietly sends the request
	// anonymously even when the URL already contains x-access-token
	// basic auth. /bin/false forces git to either use the URL creds or
	// error out cleanly.
	base := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false")
	cmd.Env = append(base, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := scrubTokens(strings.TrimSpace(string(out)))
		return "", fmt.Errorf("git %s: %w — %s", args[0], err, msg)
	}
	return string(out), nil
}

// scrubTokens removes any PAT embedded as https://x-access-token:TOKEN@host/...
// from error output so tokens do not leak into logs or UI toasts. Anything
// matching "x-access-token:<secret>@" gets replaced with "x-access-token:***@".
func scrubTokens(s string) string {
	const marker = "x-access-token:"
	for {
		i := strings.Index(s, marker)
		if i < 0 {
			return s
		}
		j := strings.IndexAny(s[i+len(marker):], "@ \t\n")
		if j < 0 {
			return s[:i+len(marker)] + "***"
		}
		s = s[:i+len(marker)] + "***" + s[i+len(marker)+j:]
	}
}

// sanitizeGitOutput strips the token from error messages when token auth
// is in use, so we don't leak secrets to logs or toast messages.
func sanitizeGitOutput(out string, auth GitAuth) string {
	s := strings.TrimSpace(out)
	if auth.Mode == GitAuthToken && auth.Token != "" {
		s = strings.ReplaceAll(s, auth.Token, "***")
	}
	return s
}

// shellQuote escapes a string for safe use in a shell command string
// (like GIT_SSH_COMMAND). Minimal — only covers space/quotes.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
