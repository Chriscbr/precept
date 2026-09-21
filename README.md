# Precept

Precept is a lightweight, agent-powered approach to informal verification.

What does that mean? Basically, Precept is a command-line tool that extracts comments in your source code that describe invariants, preconditions, assertions, and other properties, and attempts to verify each property (or produce a counter-example) in an independent Claude Code or Codex session. It's like an AI code review tool, but more fine grained and focused since each property or invariant is evaluated independently. Today, the only supported language is Go, but support for other languages is planned.

## Why?

I started Precept while thinking about how coding agents could help us build more reliable software without simply giving them larger and larger code-review prompts.

Asking an agent to review an entire pull request can be useful, but it is open-ended: the agent must decide which properties matter and how deeply to investigate them. I wanted something more fine-grained and user-controlled. The engineer should identify the property that matters, and the agent should focus on checking that property.

There are many useful claims that engineers already reason about this way. For example, you may have seen code comments like these before:

```go
// POSTCONDITION: either the returned list is sorted, or an error should be returned
```

```go
// POSTCONDITION: a failed call should leave the existing record unchanged
```

```go
// INVARIANT: Close() should be called on every resource acquired with Open()
```

These properties can be difficult to express in type systems or with conventional static analyzers. Tests can cover representative cases, but they may not capture every branch, caller, or implementation. In practice, there can be many properties that only survive in someone’s memory, a review discussion, or a decision document far from the relevant code.

You could manually ask a coding agent to re-check each property whenever the code changes, but that workflow is clumsy and easy to forget. Precept is simple automation around that idea: you can write a claim next to the code, discover it consistently, and verify it in a focused, independent agent session.

Precept was also inspired by [Aristo](https://github.com/aretta-ai/aristo), which explores verifiable intent embedded in Rust code and supports more complex verification strategies like generating structured proofs. Precept takes a deliberately smaller approach to start: it uses ordinary code comments, and it writes no proof or artifacts into your code repository.

Precept is not a theorem prover. The verification results it returns are best-effort, and should be treated as advisory. The goal is to make important assumptions explicit, colocated, and easy to investigate again as a codebase changes.

## Quick start guide

### Step 0: Install

```bash
go install github.com/Chriscbr/precept@latest
```

Precept relies on an agent CLI being installed on your system, so install and set up at least one of the supported harnesses before running it:

- [claude](https://code.claude.com/docs/en/quickstart)
- [codex](https://learn.chatgpt.com/docs/codex/cli)

### Step 1: Write claims

First, add markers to your code to describe the properties (called "claims") that you want to verify. For example:

```go
// Clamp constrains value to the requested range.
//
// PRECONDITION: lo <= hi
// POSTCONDITION: the result is within the inclusive range [lo, hi-1]
func Clamp(value, lo, hi int) int {
	return max(lo, min(value, hi))
}
```

A claim is a comment starting with one of the words `PRECONDITION:`, `POSTCONDITION:`, `ASSERTION:`, or `INVARIANT:`.
Each claim will be associated with the nearest function, constant, struct, interface, or package, and will be verified independently.

For this example, I've included a mistake in the POSTCONDITION claim to demonstrate what happens when a claim is violated.
(It should be `hi` instead of `hi-1`.)

### Step 2: Verify claims

Run the `verify` subcommand and pass a `--harness` ("claude" or "codex") to verify the claims you wrote:

```text
$ precept verify --harness claude ./example
```

When verification finishes, you should see the verification results in the output.
For the example above, one of the claims is violated and the other holds:

```text
example.Clamp [PRECONDITION] (clamp-precondition-1)  ✓ HOLDS (14s)
  Source  example/example.go:5

example.Clamp [POSTCONDITION] (clamp-postcondition-1)  ✗ VIOLATED (21s)
  Source  example/example.go:6
  Claim   the result is within the inclusive range [lo, hi-1]
  Reason  Clamp can return hi, but the claimed inclusive range ends at hi-1.

  Counterexample
    With value=10, lo=0, and hi=10, the precondition 0 <= 10 holds, but Clamp returns 10, which is outside the inclusive range [0, 9].

  Evidence
    example/example.go:5-8
    The precondition permits lo <= hi, while the implementation computes max(lo, min(value, hi)); when value >= hi, this returns hi rather than a value at most hi-1.

  Resume  codex resume 01a0c242-e79e-7152-b376-42860a7f6a51 (View in ChatGPT)

2 claims: 1 holds, 1 violated, 0 inconclusive, 0 errors
Finished in 21s. Exit code: 1
Log: /tmp/precept-verify-20260921T043918Z-717812288.log
```

That's it! You've verified your claims with an agent.
Try editing the code (or claims) and verify again to see how the results change.
Also try passing the `--model` and `--effort` flags to verify claims with different models and reasoning efforts.

## Writing claims

The supported claim markers are `INVARIANT`, `PRECONDITION`, `POSTCONDITION`, and `ASSERTION`, optionally followed by a claim ID before the colon. These markers have the following meanings:

- `// PRECONDITION:`: describes a property that is expected to hold before a function is called, or before a block is entered.
- `// POSTCONDITION:`: describes a property that is expected to hold whenever a function returns, or a block is exited, assuming that all preconditions hold.
- `// ASSERTION:`: describes a property that is expected to hold at a particular point in the code.
- `// INVARIANT:`: describes a property that is expected to hold on the boundaries of reachable paths (e.g. at the start and end of a function, method, or block).

Every claim is associated with the most relevant source subject:

- A function or method doc comment belongs to that function or method.
- A named type, constant, or variable doc or trailing comment belongs to that declaration.
- A comment in a function or method body belongs to the enclosing function or method.
- A field doc or trailing comment belongs to that named struct field.
- A free-standing claim within a named type belongs to that type.
- Any other claim belongs to its package, including free-standing package-level comments.

Give a claim an ID by putting one ASCII space and the ID between the marker and the colon:

```go
// INVARIANT buffer-single-owner: the buffer has exactly one owner
// POSTCONDITION buffer-closed: Close releases all resources
```

IDs are case-sensitive. They start with a letter or digit and may contain letters, digits, hyphens, underscores, and periods, with no whitespace. Claim IDs must be unique within each Go file; the same ID may be reused in different files.

Unnamed claims will automatically receive an ID such as `newbuffer-precondition-1` or `buffer-close-postcondition-2`. Inserting or reordering claims of the same subject and type can change default ID, so it is recommended to give claims an explicit ID if this is important.

Claims may span consecutive non-empty line comments:

```go
// PRECONDITION: do XYZ
// this is also part of the previous condition
// and this
//
// this is not part of the claim because the blank comment ended it
```

Claim markers inside of block comments (`/* ... */`) are ignored.

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
example.Clamp [PRECONDITION] (clamp-precondition-1)
  Source  example/clamp.go:3
  Claim   lo <= hi

example.Clamp [POSTCONDITION] (clamp-postcondition-1)
  Source  example/clamp.go:4
  Claim   the result is within the inclusive range [lo, hi]

2 claims in 1 file
```

Use `precept list --compact` for an index without claim descriptions:

```text
ID                     KIND           SYMBOL         SOURCE
clamp-precondition-1   PRECONDITION   example.Clamp  example/clamp.go:3
clamp-postcondition-1  POSTCONDITION  example.Clamp  example/clamp.go:4

2 claims in 1 file
```

Use `precept list --json` for machine-readable output.

If multiple claims in the same file share an ID, `precept list` will exit with code 2 and print each duplicate ID and its file on stderr.

## Verify claims

Verify all claims in a file-or-directory scope with a coding agent. Select the required agent harness with `--harness claude` or `--harness codex`:

```bash
precept verify --harness claude ./example/internal
precept verify --harness codex --jobs 6 --timeout 15m ./example/api
```

Use `--claim` to verify all claims matching an ID from `precept list` across the selected scope, or repeat it to select several IDs:

```bash
precept verify --harness codex --claim buffer-single-owner ./example
precept verify --harness codex --claim newbuffer-precondition-1 --claim buffer-close-postcondition-2 ./example/buffer.go
```

If an ID is missing, `precept verify` will error immediately. Duplicate IDs within any Go file in the selected scope also cause an error before any claims are verified. Reusing an ID across different files is allowed (though discouraged).

Optional model and reasoning-effort overrides are mapped to the selected CLI:

```bash
precept verify --harness claude --model sonnet --effort high ./example
precept verify --harness codex --model gpt-5.4 --effort high ./example
```

Append context to every verification run:

```bash
precept verify \
  --harness codex \
  --append-prompt 'Treat database rows as potentially stale unless the code proves otherwise.' \
  --append-prompt-file /tmp/precept-verifier-context.md \
  ./example
```

Precept prints claim's result as soon as its agent finishes:

```text
Verifying 2 claims in example

  Agent harness  codex
  Model          (default)
  Effort         (default)
  Workers        up to 4
  Timeout        10m per claim
  Extra context  (none)

example.Clamp [PRECONDITION] (clamp-precondition-1)  ✓ HOLDS (1s)
  Source  example/clamp.go:11

(*Cache).Get [INVARIANT] (cache-get-invariant-1)  ✗ VIOLATED (3s)
  Source  example/cache.go:42
  Claim   Missing keys are never reported as cache hits.
  Reason  The zero value is reported as a hit.

  Counterexample
    Get with an absent key returns (_, true).

  Evidence
    example/cache.go:46
    The method always returns true.

  Resume  codex resume <cache-session> (View in ChatGPT)

2 claims: 1 holds, 1 violated, 0 inconclusive, 0 errors
Finished in 3s. Exit code: 1.
Log file: /tmp/precept-verify-20260920T123456Z-3872649102.log
```

When `precept verify` is run in an interactive shell, the output is condensed to only show detailed output for failed claims. Full details about each are still available in the generated log file.

If you are using Codex as your agent harness, resume commands referenced in the output include a link to view the session in the ChatGPT desktop app if you have it installed.

Use `--format text|json|markdown` to select the output format. Text is the default.

Use `--output PATH` (or `-o PATH`) to write the output to a file (replacing its contents if it already exists). The default, `--output -`, writes to stdout. The parent directory must already exist.

```bash
# Save a report for a CI artifact or PR comment
precept verify --harness codex --format markdown --output precept-report.md ./example
# Save structured results
precept verify --harness codex --json -o precept-report.json ./example
```

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

- [ ] Add an `--changed` flag to the `list` and `verify` commands to only list or verify claims in which the claim or the code associated with the claim (function, struct, etc.) has changed in the current git branch.
- [ ] Add an `--affected` flag to the `list` and `verify` commands to conservatively list or verify claims that could be affected by the changes in the current git branch, directly or by the transitive dependencies of the changed code. (It would be useful to explain why each claim was affected: "queue.Buffer selected because: ... (1) internal/queue/delivery.go:84 changed ... (2) Buffer.StartDelivery calls delivery.Start").
- [ ] Build tooling for automatically running Precept in a CI pipeline / as a GitHub PR review bot.
- [ ] Add a `prompt` or `inspect` command to view the prompt that will be sent to the agent.
- [ ] Add a `compare` command to compare the JSON output of two verification runs for the purpose of identifying new violations, moved claims, agent verdict changes, etc.
- [ ] Add a `resolve` command (and optional `--resolve` flag on `verify`) to automatically resolve claims that don't hold by suggesting fixes or resolutions based on the context of the codebase. The proposed resolutions could be categorized into `change-code`, `change-claim`, `clarify-claim`, `split-claim`, `move-claim`, `remove-claim`, `add-enforcement`, `add-context`, `defer-to-author`, `not-verifiable`, etc.
- [ ] Add a `precept.toml` file to the project root to configure the Precept CLI's default behavior (so that users don't have to pass the same flags over and over again).
- [ ] Interactively prompt the user for options (like the harness selection, model selection, reasoning effort, etc.) when the user runs `precept verify` without any flags.
- [ ] Allow the harness's allowed tools and available MCPs to be configured via flags (e.g. `--allow-tools search,tools.search`) or in the `precept.toml` file.
- [ ] Generate an HTML report of the verification results for easier review and sharing. Configurable in the `precept.toml` file.
- [ ] Support Rust as an additional programming language.
- [ ] Support Python as an additional programming language.
- [ ] Support TypeScript as an additional programming language.
- [ ] Support opencode as an additional agent harness.
- [ ] Support pi as an additional agent harness.
