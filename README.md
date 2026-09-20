# Precept

Precept is a stateless CLI for asking agents check invariants and other natural language claims in your source code. It discovers preconditions, postconditions, and assertions in a selected scope, associates each claim with a type declaration or function, and verifies every claim in an independent Claude Code or Codex session. Today the only supported language is Go, but support for other languages is planned.

## Install

Build and install from source:

```bash
go install github.com/Chriscbr/precept@latest
```

Precept relies on an agent CLI being installed on your system, so set up at least one of the following supported harnesses before running it:

- [claude](https://code.claude.com/docs/en/quickstart)
- [codex](https://learn.chatgpt.com/docs/codex/cli)

## Write a claim

Choose the marker that describes the claim:

```go
// Clamp constrains value to the requested range.
//
// PRECONDITION: lo <= hi
// POSTCONDITION: the result is within the inclusive range [lo, hi]
func Clamp(value, lo, hi int) int {
	return max(lo, min(value, hi))
}
```

All claim markers use an exact spelling: an uppercase keyword preceded by exactly one ASCII space after `//`.

```go
func (cache *Cache) Put(key string, value Value) {
	// ASSERTION: cache.entries is non-nil before it is written
	cache.entries[key] = value
}
```

Only `// INVARIANT:`, `// PRECONDITION:`, `// POSTCONDITION:`, and `// ASSERTION:` are accepted. These markers have the following meanings:

- `// PRECONDITION:`: expected to hold on entry. Precept examines in-scope callers and callees to determine if the property holds.
- `// POSTCONDITION:`: expected to hold on every normal return path, assuming the precondition holds.
- `// ASSERTION:`: expected to hold at a particular point in the code.
- `// INVARIANT:`: expected to hold on the boundaries of reachable paths (e.g. on the start and end of the function, method, or block).

Every claim is associated with the most relevant source subject:

- A function or method doc comment belongs to that function or method.
- A named type, constant, or variable doc or trailing comment belongs to that declaration.
- A comment in a function or method body belongs to the enclosing function or method.
- A field doc or trailing comment belongs to that named struct field.
- A free-standing claim within a named type belongs to that type.
- Any other claim belongs to its package, including free-standing package-level comments.

Claims may span consecutive non-empty line comments:

```go
// PRECONDITION: do XYZ
// this is also part of the previous condition
// and this
//
// this is not part of the claim because the blank comment ended it
```

A second exact marker—`// INVARIANT:`, `// PRECONDITION:`, `// POSTCONDITION:`, or `// ASSERTION:`—starts a separate claim. Block comments (`/* ... */`) are ignored.

## Discover claims

List all claims in a file-or-directory scope:

```bash
precept list ./example
precept list ./example/internal/state_machine.go
```

The command defaults to the current working directory.
It assumes code is in a Git repository and resolves paths relative to the git project root.

The output uses the same claim header as `verify`, followed by its source location and full claim text:

```text
example.Clamp [PRECONDITION]
  example/clamp.go:3
  Claim   lo <= hi

example.Clamp [POSTCONDITION]
  example/clamp.go:4
  Claim   the result is within the inclusive range [lo, hi]

2 claims in 1 file
```

Use `precept list --compact` for an index without claim descriptions:

```text
KIND           SYMBOL         SOURCE
PRECONDITION   example.Clamp  example/clamp.go:3
POSTCONDITION  example.Clamp  example/clamp.go:4

2 claims in 1 file
```

Use `precept list --json` for machine-readable output.

## Verify claims

Verify all claims in a file-or-directory scope with a coding agent:

```bash
precept verify --agent claude ./example/internal
precept verify --agent codex --jobs 6 --timeout 15m ./example/api
```

Optional model and reasoning-effort overrides are mapped to the selected CLI:

```bash
precept verify --agent claude --model sonnet --effort high ./example
precept verify --agent codex --model gpt-5.4 --effort high ./example
```

Append context to every verification run:

```bash
precept verify \
  --agent codex \
  --append-prompt 'Treat database rows as potentially stale unless the code proves otherwise.' \
  --append-prompt-file /tmp/precept-verifier-context.md \
  ./example
```

Precept prints the invocation settings once, then each claim's result as soon as its agent finishes. Results appear in completion order, so a slow claim does not hold up other results. For example:

```text
Verifying 2 claims in example

  Agent       codex
  Model       (default)
  Effort      (default)
  Workers     up to 4
  Timeout     10m per claim
  Context     (none)

example.Clamp [PRECONDITION]  ✓ HOLDS (1s)
  example/clamp.go:11
  Claim   The lower bound does not exceed the upper bound.
  Reason  Every caller establishes lo <= hi before calling.

  Evidence
    example/caller.go:8-10
    The caller checks both bounds before calling.

  Resume  codex resume <clamp-session>

(*Cache).Get [INVARIANT]  ✗ VIOLATED (3s)
  example/cache.go:42
  Claim   Missing keys are never reported as cache hits.
  Reason  The zero value is reported as a hit.

  Counterexample
    Get with an absent key returns (_, true).

  Evidence
    example/cache.go:46
    The method always returns true.

  Resume  codex resume <cache-session>

2 claims: 1 holds, 1 violated, 0 inconclusive, 0 errors
Finished in 3s. Exit code: 1.
```

Each result header includes that claim's verification duration, excluding time waiting for a worker, in gray parentheses. Durations use forms such as `18s`, `1m12s`, and `<1s` for checks that finish in under a second.

`Model` and `Effort` show `(default)` when no override was passed to the selected CLI; Precept does not infer the model or effort that the agent will choose. These defaults are gray in a color terminal. `Context` shows the number of non-empty text prompts and files appended to each verification prompt, without printing their contents.

In an interactive terminal, `list` and `verify` show a gray scan spinner only if discovery takes longer than 200 ms. It disappears when discovery finishes; warnings remain visible. During verification, a live footer shows completed, active, and queued checks, elapsed time, and active claim locations. Completed claim blocks stay in scrollback. The footer disappears before the final summary, and interrupted runs preserve completed results and report how many checks remain unfinished. An empty scope produces a short summary without starting an agent.

Results go to stdout; progress, discovery warnings, operational diagnostics, and the final verification-log path go to stderr. Each stream detects its own terminal status. Build logs and other non-terminal streams use plain output without cursor movement: discovery prints a start and completion line, and long scans or verification runs print a heartbeat after 30 seconds without other output. `NO_COLOR` disables color, and `TERM=dumb` also disables animation.

Use `precept verify --json` for one final JSON document on stdout. Its outcomes remain in source discovery order even when checks finish out of order. Progress stays on stderr. The JSON `agent.version` field is empty because Precept checks executable availability without launching a `--version` probe.

If the selected agent executable is missing, `verify` fails before scanning or loading appended context. Once verification begins, an agent process failure, crash, timeout, or invalid response becomes `ERROR` for that claim while other claims continue.

See `precept verify --help` for all options.

## Verdicts and exit codes

- `holds`: the agent found the claim consistent with the relevant code.
- `violated`: the agent found a concrete path or counterexample that breaks it.
- `inconclusive`: the available code is insufficient to establish or refute it.
- `error`: the claim marker is strictly incompatible with the claim's intended meaning, or discovery or verification failed operationally.

Agent verdicts are advisory - that is to say, they're not rigorous mathematical proofs.

## Safety model

Precept passes prompts without a shell and configures each harness to only have access to read-only operations. Claude receives only read/search tools. Codex runs with its read-only sandbox and no approvals.

Agent sessions are persisted in each CLI's normal local session store so that they can be resumed later for further investigation. Logs about verification runs are generated under `/tmp`, and Precept does not write any validation state into the project's source code.

For isolation, Codex runs with user configuration disabled while retaining normal CLI authentication. That means a model selected in `config.toml` is not used; pass `--model` explicitly when needed.

The coding-agent binaries and their sandboxes are external dependencies - they're not bundled with Precept or automatically installed. Review their installed versions and local policy configuration as part of adopting Precept in a sensitive workflow.

## Development

Run the test suite:

```bash
go test ./...
```

## Roadmap

Here are some ideas for future features:

- [ ] Add an `--changed` flag to the `list` and `verify` commands to only list or verify claims in which the claim or the subject (function, struct, etc.) has changed in the current git branch.
- [ ] Add an `--affected` flag to the `list` and `verify` commands to conservatively list or verify claims that could be affected by the changes in the current git branch, directly or by the transitive dependencies of the changed code. (It would be useful to explain why each claim was affected: "queue.Buffer selected because: ... (1) internal/queue/delivery.go:84 changed ... (2) Buffer.StartDelivery calls delivery.Start").
- [ ] Build tooling for automatically running Precept in a CI pipeline / as a GitHub PR review bot.
- [ ] Add a `prompt` or `inspect` command to view the prompt that will be sent to the agent.
- [ ] Add a `compare` command to compare the JSON output of two verification runs for the purpose of identifying new violations, moved claims, agent verdict changes, etc.
- [ ] Add a `resolve` command (and optional `--resolve` flag on `verify`) to automatically resolve claims that don't hold by suggesting fixes or resolutions based on the context of the codebase. The proposed resolutions could be categorized into `change-code`, `change-claim`, `clarify-claim`, `split-claim`, `move-claim`, `remove-claim`, `add-enforcement`, `add-context`, `defer-to-author`, `not-verifiable`, etc.
- [ ] Support user-defined claim IDs (e.g. "// INVARIANT buffer-single-owner: ...") to give claims stable names for use in the `--claim` flag and in CLI outputs. Unnamed claims should be given a stable default name (e.g. "newbuffer-precondition-1", "buffer-close-postcondition-2", etc.) based on the position of the claim in the file and the claim type.
- [ ] Add a `--claim` flag to the `verify` command to only verify a specific claim (may be repeated to verify multiple individual claims).
- [ ] Add a `precept.toml` file to the project root to configure the precept CLI's default behavior (so that users don't have to pass the same flags over and over again).
- [ ] Interactively prompt the user for options (like the harness selection, model selection, reasoning effort, etc.) when the user runs `precept verify` without any flags.
- [x] Improve the readability of the `list` command's output.
- [ ] Allow the harness's allowed tools and available MCPs to be configured via flags (e.g. `--allow-tools search,tools.search`) or in the `precept.toml` file.
- [ ] Support programming languages other than Go.
