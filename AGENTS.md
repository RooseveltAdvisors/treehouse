# Treehouse - Agent Guide

## What is this?

Treehouse is a Go CLI tool that manages a pool of git worktrees (or, with the opt-in jj backend, Jujutsu workspaces) for parallel AI coding agent workflows. It maintains reusable, pre-warmed worktrees so agents get isolated environments instantly.

## Project Structure

- `main.go` - entry point, calls `cmd.Execute()`
- `cmd/` - CLI commands (cobra): `get` (incl. `get --lease`), `enter`, `return`, `status`, `prune`, `destroy`
- `internal/config/` - config file loading (`treehouse.toml`)
- `internal/hooks/` - user-configured lifecycle hook command execution
- `internal/pool/` - pool manager (acquire, release, list, destroy, prune) + state file
- `internal/vcs/` - VCS backend seam: the `vcs.Backend` interface, backend selection (`backendFor`), and package-level wrappers the rest of the code calls
- `internal/vcs/gitvcs/` - git backend (shells out to `git` binary)
- `internal/vcs/jjvcs/` - Jujutsu backend (shells out to `jj` binary; pooled worktrees are jj workspaces)
- `internal/process/` - in-use detection and lingering process termination for worktrees
- `internal/shell/` - subshell spawning
- `internal/ui/` - Y/n confirmation prompts

## Building

```sh
go build -o treehouse .
# or
make build
```

## Testing

```sh
go test ./...
# or
make test
```

## Key Design Decisions

- No daemon - all operations are inline CLI commands
- Detached HEAD worktrees reset to whichever of local or origin default branch is further ahead (prefers origin on divergence)
- Acquire reuses only an idle, unleased, clean slot whose HEAD is merged into its exact immutable reset target, or into its recorded base via `headMergedIntoRecordedBase` (which re-reads HEAD, fails closed on mismatch/error, then resets through the requested base commit). Inferred acquisitions and state predating `base_branch` record no base: acquire alone may treat the repository default as implicit because HEAD remains reachable from a local branch; prune/destroy never do, using the origin-validated default for deletion. Acquire records HEAD, re-reads it under a lock concurrent git/jj writes cannot bypass (git `HEAD.lock`; jj one `rebase`/`abandon` revset `@ & commit_id(expected)`), then rechecks dirtiness before destructive update. It skips lock failure, HEAD changes, or new dirtiness, and fails closed when dirty, unverifiable, or merge state is unproven. `return` still discards dirty trees (`requireClean=false`).
- Base is opt-in: `base_branch` in `treehouse.toml` (repo config wins user config) or per-invocation `treehouse get --base <branch>` (flag wins config); both empty preserve inference. `pool.resolveBaseBranch` runs after fetch and feeds `AddWorktree`, `IsWorktreeSafeToReset`, and `ResetWorktreeToRef`. Explicit requests require `vcs.VerifyBaseBranch` → `gitvcs.BranchExists`, matching only `refs/heads/<b>` or `refs/remotes/origin/<b>` (the refs `branchRef` selects): names only, never tags, SHAs, `origin/<b>`, or literal `HEAD` (git clone creates `refs/remotes/origin/HEAD`). Verification is mandatory: acquire skips unsafe slots rather than silently burning new ones. `VerifyBaseBranch` is outside `Backend` and rejects non-git backends because an untested implementation must not enter destructive paths. `gitvcs.branchRef` returns fully qualified refs so a same-named tag cannot win. `WorktreeEntry.BaseBranch` records only explicit bases; prune/destroy use it for a second merge reading, so inferred defaults never widen deletion. `ReleaseConditional` parks on the cut base in this order: configured base (never invocation `--base`), recorded `WorktreeEntry.BaseBranch`, repository default; it clears the field on default parking. Without recorded fallback, `--base`-only pools never recycle. Resolve default before state lock so errors precede `beforeReset`; only missing requested base makes this fatal. If requested reset fails, warn and park on default. `LeaseInfo.BaseBranch` reports resolved base (`get --lease --json`); human `status` prints it, while `status --json` remains one top-level array. Acquire, parking, prune, and destroy share one-base-per-slot invariant. `pool.headLandedOnItsBase` retries merge against `WorktreeEntry.BaseBranch` via `vcs.BaseBranchMergeRef` → `gitvcs.BranchMergeRef` (git-only), only after a definitive default-ref "not merged" and only for explicit bases; empty base preserves non-opt-in deletion semantics, and unverifiable default checks remain fail-closed.
- In-use detection uses process scanning plus short-lived persisted owner reservations for lifecycle operations
- Durable leases are process-independent: `WorktreeEntry.Leased`/`LeaseID`/`LeaseHolder`/`LeasedAt` persist with `omitempty`; each acquisition creates immutable random 128-bit `LeaseID`. Legacy state without ID remains releasable through unconditional path. Live processes do not derive leases; zero processes do not clear them and `healState` never does. Acquire/prune skip leases; destroy classifies `DestroyLeased` and removes one only when exact path names it with `--include-leased`, never via `--all`; status shows `StatusLeased`; `Release` (`return`) clears it. `treehouse lease <name>` (`pool.LeaseExisting`) state-only stamps an existing registered slot via `markAcquired`, refuses unknown/already-leased slots, and does no reset/fetch/clean/checkout; `get --lease` protects only slots it allocates.
- `destroy` mirrors safe-by-default `prune`: dry-run without `--yes`; targets are one `destroy <path>` or one-pool `destroy <pool> --all`; no cross-pool/global destroy, and `--all` needs a pool. Removed v2.0.0 `--force`; opt-ins are `--include-unlanded` (dirty/unmerged/unverified), `--include-in-use` (process/owner reservation; terminate processes cleanly first), and `--include-leased` (leased, named path only). Bare `--all --yes` removes only merged, clean, idle, unleased slots; bulk skips exit 0, single-target skips non-zero. Entry points are `pool.DestroyWorktree` (`allowLeased=true`) and `pool.DestroyPool` (`allowLeased=false`). Both use `classifyForDestroy` in `internal/pool/destroy.go`, sharing prune primitives (`ownerAlive`, `process.FindProcessesInWorktree`, `backingRepositoryMissing`, `vcs.IsDirty`, `vcs.IsHeadMergedIntoRef` against `resolvePruneDefaultRef`) and two-phase reservation (flock, `pre_destroy`, remove only while `sameDestroyReservation` holds), so hook-time reacquisition cannot be deleted.
- `get --lease` (see `getLeaseRunE`) is the non-interactive acquire: it opens no subshell, routes hook output and banners to stderr, and keeps path-only stdout unchanged. `get --lease --json` returns `pool.AcquireLeaseInfo`, and `status --json` exposes the same `lease_id`, holder, and timestamp. Conditional return uses `pool.ReleaseConditional` with `--if-lease-id` and optional `--if-lease-holder`; comparison, caller-side preparation, reset, and final clear share one `WithStateLock`, while return without conditions keeps the legacy path-only behavior
- Dirty checks include untracked files even when repository config hides them from normal `git status` output
- Prune deletes only idle managed worktrees that are clean and whose HEAD is merged into the default branch; dry run is the default
- Prune reports unsafe idle worktrees in grouped, stable categories and keeps raw VCS diagnostics for verbose output instead of default output
- Prune treats backing-repository-missing linked worktrees as orphans in both flavors (a git `.git` gitdir pointer or a jj `.jj/repo` store pointer naming a deleted directory; a `.jj/repo` directory is a main workspace and never an orphan); they are only deletable with explicit `--prune-orphans --yes`, and each candidate warns that content could not be verified
- Prune never treats an unreachable origin as a deletable orphan; those worktrees stay skipped because the repository may still be valid. Each backend owns its unreachable-origin error vocabulary (`gitvcs`/`jjvcs` `IsOriginAccessError`; jj shells out to git so its patterns wrap git's), and the `vcs` facade classifies by error content, not by the configured backend
- Global prune enumerates managed pool directories under the user-level treehouse root and derives each worktree's owning repository from VCS metadata instead of relying on the current directory
- Global prune loads user-level config and hooks only because it can run without a repository context
- State file tracks pool membership, temporary owner/destroy reservations, and explicit durable leases.
  It still does not infer long-term usage from processes.
- `WriteState` is atomic: it writes to a temp file in the pool directory, fsyncs it, commits it with the platform replacement primitive, and syncs the parent directory where supported.
  A crash mid-write can never leave a truncated or empty state file.
  `ReadState` treats a state file that exists but fails to parse (empty or truncated) as recoverable rather than a hard failure: it prints a loud warning to stderr and rebuilds a `State` by scanning the pool directory for worktree subdirectories still on disk (`recoverCorruptState` in `internal/pool/state.go`).
  Since the real reservation (owner vs. lease vs. idle) is unknowable from disk alone, every recovered entry is marked `Leased` with a `recoveredLeaseHolder` placeholder.
  `Acquire` and `prune` skip recovered entries, and `destroy` only removes one via a single named `--include-leased` target.
  A human clears a recovered entry with `treehouse status` then `treehouse return` (or `destroy --include-leased`) once verified
- All VCS operations use `internal/vcs` (`vcs.Backend`, 19 operations), whose git/jj backends shell out to binaries (go-git worktree support is incomplete). Git remains default, including colocated (`.jj`+`.git`) and `.jj`-only repos. jj requires `vcs = "jj"` or `TREEHOUSE_VCS=jj` (precedence env, repo config, user config), only where `.jj` exists; unknown values keep git and emit one deduped stderr warning with value/source. Pooled jj workspaces are `.jj`-only and cannot carry untracked config: `backendFor` reads `.jj/repo` then main-root config, never a marker. Existing-slot facts/actions dispatch by its marker (`slotMarkerBackend`: `.git` wins, then `.jj`): `backendForWorktree` routes `IsDirty`, `IsHeadMergedIntoRef` with `DefaultBranchMergeRefForWorktree`, `ResetWorktree`, `ResetWorktreeToRef`, `IsWorktreeSafeToReset`, `DetachWorktree`, `FindMainRepoRootFrom`, and release `DefaultBranchForWorktree`; `backendForRemoval` routes `RemoveWorktree`/`RemoveCleanWorktree`, falling back to `backendFor(repoRoot)` when missing. This is artifact-typed dispatch, not marker opt-in: markerless ordinary dirs and worktree creation use configured backend, and configured backend never answers for another flavor. Acquire reuses only marker-matching slots and creates selected flavor; other flavors stay untouched, count toward `max_trees`, show as `WorktreeStatus.Flavor`, and migrate via destroy/re-get. Markerless slots fail closed: acquire never reuses; release clears lease without discovery/reset/detach; status says `damaged` without facts; prune cannot-verify without fallback facts; destroy marks unverified and removes plain directory only with `--include-unlanded`; destructive wrappers (`ResetWorktree`, `ResetWorktreeToRef`, `DetachWorktree`) reject markerless paths.
- jj semantics: dirty means `@` non-empty or described; reset is `jj abandon -r @` then `jj new <default>` (recoverable with `jj op restore`); merged means ancestry revset `@- & ~::<ref>` empty, so squash-merged work reads unmerged. Default resolution prefers origin bookmarks `main`/`master`/`trunk`, never bare `trunk()`, and falls back to `root()` without remotes. Workspace store pointers are canonicalized absolute paths on write and both reads, making symlinked paths one pool identity. `PruneWorktrees` is documented no-op with add-time self-healing. `RemoveWorktree` refuses existing non-jj or main workspaces (`.jj/repo` is store, not pointer), preventing deletion of git files/repository; missing paths still forget stale registrations. jj tests isolate `JJ_CONFIG` (`git.colocate=false`) and skip without `jj` on PATH.
- The pool root is made self-ignoring (`.gitignore` containing `*` written inside it) because non-colocated jj repos never read `.git/info/exclude`; inside git repos the `info/exclude` entry is still added too (`config.EnsureExcluded`)
- Self-healing: stale state entries are auto-removed, and `get` prunes stale worktree registrations before adding a worktree (the git backend via `git worktree prune`; the jj backend by forgetting a stale same-path workspace registration at add time)

## Contribution Gate

- PRs to `main` must carry the no-mistakes pipeline signature and a v1 pipeline step attestation whose `head_sha` matches the current PR head (`review`, `test`, and `document` each `status=completed`). Signature-only bodies from no-mistakes older than 1.46.0 fail. The required check is `PR must be raised via no-mistakes` (`.github/workflows/no-mistakes-required.yml`), enforced by the repository `main` ruleset; only the owner/Admin role can bypass it.
- The required check is decided by the SHARED composite action `kunchenguid/no-mistakes/.github/actions/require-no-mistakes`, pinned to an immutable commit (never `@main`, which the judged PR could edit). Bot exemptions ride as its `exempt-authors` input, deliberately not a job-level `if:`: a skipped job never reports a required context, so the PR would block on a status that can never arrive. Bumping the pin is a separate, deliberate PR.
- `.github/scripts/no-mistakes-gate.sh` survives for ONE consumer: `release.yml`'s `release-pr-gate-status` job (see the next bullet). Its structural release-please test is therefore still load-bearing, not dead code, and `TestNoMistakesGateDecisions` drives the script directly.
- release-please opens its PRs with `GITHUB_TOKEN`, so GitHub creates **no** workflow runs on them and the gate can never report there. `release.yml`'s `release-pr-gate-status` job publishes the required context on the release PR head by running that same gate script, so release PRs go green without an owner override.
- A workflow backing a required check must never use `paths`/`paths-ignore`: a filtered required check never reports and blocks the PR forever. `TestPullRequestWorkflowsExcludeReleasePleaseOutputs` encodes both that rule and the opposite rule for ordinary PR workflows.

## Windows Compatibility

This project targets Linux, macOS, and Windows. All new code **must** work on Windows. Follow these rules:

- **Paths**: Never hardcode `/` as a path separator. Use `filepath.Join()`, `filepath.Separator`, or `filepath.ToSlash()` as appropriate.
- **Shell**: Do not assume `/bin/sh` or `$SHELL` exist. On Windows, use `%COMSPEC%` (usually `cmd.exe`). See `internal/shell/shell.go` for the pattern.
- **Syscalls**: Unix-only syscalls (e.g., `syscall.Flock`) must be isolated behind build tags (`//go:build !windows` / `//go:build windows`). See `internal/pool/lock_unix.go` and `lock_windows.go` for the pattern.
- **Build tags**: Follow the existing `_unix.go` / `_windows.go` naming convention (see also `internal/updater/sysproc_*.go`).
- **CI**: The CI matrix runs tests on `ubuntu`, `macOS`, and `windows`. Cross-compile locally with `GOOS=windows go build ./...` to catch issues early.
- **Process detection**: `gopsutil` is cross-platform - no special handling needed, but avoid importing platform-specific process APIs directly.

## Config

Place repo-safe settings in repo root `treehouse.toml` or user-level `~/.config/treehouse/config.toml`:

```toml
max_trees = 16

# Optional worktree root.
# Relative roots need a repo context; use an absolute user-level root for global prune.
# root = "$HOME/worktrees"

# Optional VCS backend. Git is the default everywhere; "jj" opts in to the
# Jujutsu backend (or per-command with TREEHOUSE_VCS=jj).
# vcs = "jj"

# User-level config only:
[hooks]
post_create = ["./scripts/setup-venv.sh"]
pre_destroy = ["./scripts/teardown.sh"]
```

Hooks are ignored in repo-level config for safety.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
