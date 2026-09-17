# Precept

Precept is a stateless CLI that asks an agent to check behavioral claims included in Go code comments. It discovers preconditions, postconditions, assertions, and invariants in a selected scope, associates each claim with a Go declaration or function, and verifies every claim in a fresh read-only Claude Code or Codex session.

## Install

Build and install from source:

```bash
go install github.com/Chriscbr/precept@latest
```

Precept launches an existing, current agent CLI, so install and authenticate at least one supported harness before running it:

```bash
claude --version
codex --version
```

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

The file-or-directory argument is optional and defaults to the current working directory. Precept assumes code is in a Git repository and resolves paths relative to the git project root.

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

When the verification run is complete, Precept prints the results:

```text
✓ queue.Buffer [INVARIANT] (internal/queue/buffer.go:33)
  [outcome]: Holds
  [reason]: In the queue processor, PendingItems is read or mutated only before the processing goroutine starts, by that one goroutine, or after Stop waits for it to exit. Background delivery workers operate on a detached item slice, not PendingItems.
  [supporting evidence]:
    • Start reads the field before it launches b.process in a new goroutine. (internal/queue/buffer.go:62-68)
    • The processing loop performs normal inserts, length checks, delivery initiation, and retry merges serially in its one goroutine. (internal/queue/buffer.go:78-115)
    • Stop waits for b.process to finish before it calls StartDelivery, so its final delivery cannot overlap accesses by process. (internal/queue/buffer.go:119-124)
    • StartDelivery copies values from PendingItems and replaces the map before launching its asynchronous worker; the worker receives only the local item slice. (internal/queue/buffer.go:138-145)
    • State changes synchronously stop the previous state before initializing the next one, preventing overlapping Buffer Start/Stop lifecycle calls. (internal/queue/state_machine.go:261-270)
  [agent session]: codex resume <session-id>

✗ queue.BuildBatches [INVARIANT] (internal/queue/batcher.go:444)
  [outcome]: Violated
  [reason]: A single item larger than the request-size limit is still added to a batch, so BuildBatches can return a batch whose total size exceeds maxRequestSizeBytes. It also permits one item when maxItemsPerBatch is zero.
  [supporting evidence]:
    • When the next item would exceed either limit, the function appends the current batch and creates a new empty one, but then unconditionally appends that same item to the new batch. It does not reject or otherwise handle an item that alone exceeds maxRequestSizeBytes, nor does it re-check the limits after resetting. (internal/queue/batcher.go:457-479)
    • Any nonempty final batch is returned, including a newly created batch containing an oversized item or an item added despite maxItemsPerBatch being zero. (internal/queue/batcher.go:481-484)
  [counterexample]: Call BuildBatches with maxRequestSizeBytes set below the encoded empty request plus a valid QueueItem's size, maxItemsPerBatch=1, and one item that can be encoded. The size check resets the empty batch, then lines 477-479 add the oversized item to the new batch, which lines 481-482 return.
  [agent session]: codex resume <session-id>
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
- [ ] Improve the readability of the `list` command's output.
- [ ] Allow the harness's allowed tools and available MCPs to be configured via flags (e.g. `--allow-tools search,tools.search`) or in the `precept.toml` file.
- [ ] Support programming languages other than Go.
