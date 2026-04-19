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
	args = append(args, effectiveURL, destDir)

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
	// Fetch via a transient URL override — doesn't modify .git/config.
	if _, err := runGit(ctx, destDir, env,
		"-c", "remote.origin.url="+effectiveURL,
		"fetch", "--depth", "1", "origin", br); err != nil {
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
func GetRepoStatus(ctx context.Context, repoDir string) (*RepoStatus, error) {
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

	// Fetch to know ahead/behind (best-effort, may fail without auth)
	_ = runGitSilent(ctx, repoDir, nil, "fetch", "--quiet", "origin")

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

	// Remote URL (strip embedded tokens)
	out, err = runGit(ctx, repoDir, nil, "config", "--get", "remote.origin.url")
	if err == nil {
		u := strings.TrimSpace(out)
		if parsed, err := url.Parse(u); err == nil && parsed.User != nil {
			parsed.User = nil
			u = parsed.String()
		}
		status.RemoteURL = u
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

	if _, err := runGit(ctx, repoDir, env,
		"-c", "remote.origin.url="+effectiveURL,
		"pull", "--ff-only", "origin", branch); err != nil {
		return fmt.Errorf("pull: %w", err)
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

	_, err = runGit(ctx, repoDir, env,
		"-c", "remote.origin.url="+effectiveURL,
		"push", "origin", branch)
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
	base := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/true")
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
