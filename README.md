# plexus

**A high-density, sandboxed agent execution engine with mesh communication — shipped as a single self-contained executable.**

plexus assembles a full agent (cognitive loop + effectors + delegation + memory) on a
NATS mesh, and isolates each agent with bubblewrap via a self-reexec pattern. One
binary is the CLI, the daemon, the agent runtime, and its own sandbox launcher.

> **Status: early development.** The agent runtime (`chat`), the mesh, and the tool /
> memory subsystems are working. The hard isolation layer (per-agent filesystem +
> network enforcement) is landing incrementally — see [Sandboxing](#sandboxing) and
> [Roadmap](#roadmap). Interfaces may still change; pin a commit if you depend on one.

---

## Table of contents

- [Overview](#overview)
- [Install & build](#install--build)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Chat REPL](#chat-repl)
- [Configuration](#configuration)
- [Observability](#observability)
- [Sandboxing](#sandboxing)
- [Architecture](#architecture)
- [Project layout](#project-layout)
- [Development](#development)
- [Roadmap](#roadmap)
- [License](#license)

---

## Overview

plexus is built around a few deliberate choices (see [`AGENTS.md`](./AGENTS.md) for the
full rationale):

- **Single executable.** The same binary runs the CLI, the mesh daemon, the assembled
  agent, and — via the self-reexec pattern — its own bubblewrap sandbox. No sidecars.
- **Mesh over NATS.** Agents and the control plane talk over a NATS subject hierarchy.
  `chat` embeds a bus for a zero-setup local session; `run` connects to an external one.
  The address of that bus — the backbone agents attach to — is the **trunk** (`--trunk`).
- **Bubblewrap sandbox.** Isolation uses `bwrap` (mount / pid / ipc / user namespaces),
  not `chroot` or container libraries. A statically linked `bwrap` is embedded with
  `//go:embed` for pure single-file distribution.
- **Standard library first.** Minimal external dependencies; every NATS integration is
  `context.Context`-cancellable.

Core mesh + agent features run cross-platform; the sandbox layer (bwrap, seccomp,
cgroup, network namespaces) is **Linux-only**.

## Install & build

**Requirements**

- A recent Go toolchain (see [`go.mod`](./go.mod)).
- Linux with user namespaces enabled — for the sandbox features only. The mesh and
  `chat` run on any OS Go supports.
- [`task`](https://taskfile.dev) (optional) — the Taskfile wraps the common commands;
  the raw `go` commands below work without it.

**Build from source**

```sh
task build            # -> bin/plexus  (also runs `go build ./...`)

# or, without task:
go build -o bin/plexus ./internal/bin/plexus
```

**Build tags**

| Tag | Effect |
| --- | --- |
| _(default)_ | Full binary, including the `chat` command and its readline REPL. |
| `nochat` | Drops the `chat` command and its readline TUI dependency — leaner production builds. `go build -tags nochat ./...` |

## Quick start

The fastest way to see an agent is `chat`, which starts its own embedded NATS and a
REPL:

```sh
export OPENAI_API_KEY=sk-...        # or ANTHROPIC_API_KEY=...
bin/plexus chat                     # or: bin/plexus chat --provider anthropic
```

No key handy? `chat` starts anyway — set one in-session with `/key <value>`.

Run a mesh daemon against an external NATS, and watch its observability streams from a
second terminal:

```sh
bin/plexus run --trunk 127.0.0.1:4222 --id agent-1
bin/plexus watch agent-1            # tail sys.obs.agent-1.*
```

## Commands

Run `plexus <command> --help` for the authoritative, always-current flag list.

### Global flags

| Flag | Default | Description |
| --- | --- | --- |
| `--debug` | `false` | Enable debug logging (persistent; applies to every command). |

### `plexus run`

Run the plexus mesh daemon — a node that connects to an external NATS mesh.

| Flag | Default | Description |
| --- | --- | --- |
| `--trunk` | `127.0.0.1:4222` | Trunk (mesh bus) address to connect to, `host:port`. |
| `--id` | `agent-x` | Agent identity on the mesh. |
| `--sandbox` | `false` | Re-exec the daemon inside a strict bwrap sandbox (see [Sandboxing](#sandboxing)). |

### `plexus chat`

Chat with a fully assembled agent (brain + effectors + delegation + memory) hosted on a
self-started embedded NATS mesh, with a REPL client. A single, non-persisted session.
_Excluded from `-tags nochat` builds._

| Flag | Default | Description |
| --- | --- | --- |
| `--provider` | _(auto)_ | LLM provider: `openai` \| `anthropic`. Env: `PLEXUS_LLM_PROVIDER`. Auto-detected from an available key if unset. |
| `--model` | _(provider default)_ | Model id. Env: `PLEXUS_LLM_MODEL`. |
| `--base-url` | _(provider default)_ | Optional API base URL. Env: `PLEXUS_LLM_BASE_URL`. |
| `--reasoning` | _(off)_ | Reasoning effort: `minimal\|low\|medium\|high\|xhigh\|max`. Env: `PLEXUS_REASONING`. |
| `--system` | — | Override the default chat role card's system prompt. |
| `--allow-exec` | `false` | Enable the `run_command` effector (arbitrary shell; each call is approval-gated). |
| `--sandbox` | `false` | Run chat inside a strict bwrap sandbox (fs/namespace isolation). |
| `--trunk-port` | _(auto)_ | Pin the embedded trunk to a port; unset auto-assigns a free one, printed at startup. |
| `--debug-llm` | `false` | Print the raw LLM request body + response status. |

### `plexus watch [agent-id]`

Subscribe to the mesh's observability streams (`sys.obs.<id>.<kind>`) and print them —
the standalone monitor for the debug channels (tool/delegation trace, raw LLM,
thinking, logs). With no `agent-id`, watches every agent.

| Flag | Default | Description |
| --- | --- | --- |
| `--trunk` | `127.0.0.1:4222` | Trunk (mesh bus) address to watch, `host:port`. |
| `--kind` | _(all)_ | Filter to one obs kind: `trace` \| `raw` \| `deleg` \| `thinking` \| `log`. |

### `plexus inspect`

> **Stub.** Diagnostic probe for the NATS bus and mesh state — not yet implemented.

## Chat REPL

Inside `plexus chat`, the user is a control-plane peer (never touches the cognitive loop
directly). Slash commands:

| Command | Description |
| --- | --- |
| `/key <v>` | Set the LLM API key (starting without one is fine). |
| `/provider <p>` | Switch provider (`openai` \| `anthropic`). |
| `/model <id>` | Set the model id. |
| `/models` | List the provider's models. |
| `/system <txt>` | Set the agent's system prompt (resets history). |
| `/reasoning <lvl>` | Reasoning effort: `minimal\|low\|medium\|high\|xhigh\|max\|off`. |
| `/debug on\|off` | Show the raw LLM request body + response status. |
| `/status` | Show gateway config. |
| `/tools` | List the agent's tools. |
| `/steps` | Show the agent's plan (checkpoint chain). |
| `/memory` | Show the agent's working memory. |
| `/trace on\|off` | Verbose tool/delegation results + raw obs (alias `/verbose`). |
| `/reset` | Clear the conversation. |
| `/approve`, `/deny` | Answer a pending approval. |
| `/help`, `/exit` | Help / quit (also `Ctrl-D`). |

`Ctrl-C` resets the in-flight turn without tearing down the session.

## Configuration

### Environment variables

| Variable | Used by | Purpose |
| --- | --- | --- |
| `PLEXUS_LLM_PROVIDER` | `chat` | Default provider (`openai` \| `anthropic`). |
| `PLEXUS_LLM_MODEL` | `chat` | Default model id. |
| `PLEXUS_LLM_BASE_URL` | `chat` | Default API base URL. |
| `PLEXUS_REASONING` | `chat` | Default reasoning effort. |
| `OPENAI_API_KEY` | `chat` | OpenAI key (auto-detected when provider is unset). |
| `ANTHROPIC_API_KEY` | `chat` | Anthropic key (auto-detected when provider is unset). |

Explicit flags take precedence over the corresponding environment variable.

### Configuration files

> **TODO.** File-based configuration (role cards, per-agent sandbox policy, mesh
> topology) is coming with the control-plane work. For now, configuration is via flags
> and environment variables.

## Observability

Agents publish debug output to `sys.obs.<agent-id>.<kind>` on the mesh, kept off the
functional report channel. Kinds: `trace` (tool/delegation), `raw` (LLM), `deleg`
(delegation), `thinking`, `log`. Tail them with [`plexus watch`](#plexus-watch-agent-id),
or inside `chat` with `/trace on`. A `chat` session prints its trunk address on startup
(it auto-assigns a free port); pass that to `watch --trunk`, or pin it with
`chat --trunk-port`.

## Sandboxing

plexus isolates an agent by **re-executing itself under bubblewrap** (the self-reexec
pattern): the host process builds the sandbox and `exec`s the embedded `bwrap`, which
launches the confined agent.

**Available now**

- `plexus run --sandbox` and `plexus chat --sandbox` re-exec into a bwrap
  sandbox providing filesystem + namespace isolation (mount / pid / ipc / user
  namespaces, a read-only system view, `/dev` + `/proc` + an ephemeral `/tmp`, all
  capabilities dropped, die-with-parent).
- The isolation policy is modeled as a semantic spec lowered to bwrap arguments, so the
  binary never exposes raw, unusable flag combinations.

**Requirements & limits**

- Linux with **user namespaces** enabled. The `bwrap` binary is embedded via
  `//go:embed`; builds without an embedded binary report sandbox mode as unsupported.
- Per-agent network enforcement (namespaced egress fence) and minimal per-agent
  root filesystems are **in progress** — see [Roadmap](#roadmap).

> **Note.** In `chat` (single process, embedded control plane), `--sandbox` applies
> filesystem/namespace isolation. The network egress fence belongs to multi-agent
> cluster deployments.

## Architecture

> **TODO — expand.** A high-level map for now; a deeper architecture document will
> follow.

- **CLI** (`internal/cmd`) → **agent runtime** (`pkg/brain` cognitive loop, `pkg/effector`
  tools, `pkg/store` memory) → **mesh** (`pkg/mesh`, `server`, `protocol`) → **sandbox**
  (`sandbox/*`).
- `chat` embeds NATS + the control plane in one process for local use; `run` is a mesh
  node against external NATS.
- LLM access is abstracted behind `pkg/llm`; MCP tools behind `pkg/mcp`.

## Project layout

| Path | What |
| --- | --- |
| `internal/bin/plexus` | Executable entry point (`main`). |
| `internal/cmd` | Cobra CLI commands (`run`, `chat`, `watch`, `inspect`). |
| `internal/chat` | `chat` REPL, gateway, and control channel. |
| `internal/logger` | Logging setup. |
| `pkg/brain` | The agent cognitive loop (kernel, role card, checkpoints). |
| `pkg/effector` | Effect / tool execution and permission gating. |
| `pkg/llm` | LLM provider abstraction. |
| `pkg/mcp` | Model Context Protocol client subsystem. |
| `pkg/mesh` | Mesh node (agent ↔ control-plane transport). |
| `pkg/store` | Memory stores (checkpoint, working memory, …). |
| `pkg/jsonschema` | JSON Schema helpers. |
| `protocol` | Mesh message protocol. |
| `server` | Embedded / mesh NATS server wiring. |
| `sandbox` | Isolation: `bwrap`, `seccomp`, `cgroup`, `netpol`, rlimits, ticket, env-state describe. |
| `test` | Smoke / integration tests. |

## Development

```sh
task build          # build bin/plexus and `go build ./...`
task run            # go run ./internal/bin/plexus
task test           # go test -v -count=1 ./...
task test:smoke     # smoke tests only
task lint           # golangci-lint (must pass before tests/CI)
```

Raw equivalents (no `task`):

```sh
go build -o bin/plexus ./internal/bin/plexus && go build ./...
go test -count=1 ./...          # TestMeshCommunicationSmoke is the integration smoke
go run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run
```

Conventions (see [`AGENTS.md`](./AGENTS.md)): favor the standard library, keep external
dependencies minimal, make every NATS integration `context.Context`-cancellable, and
ensure `task lint` passes before anything else.

## Roadmap

> Skeleton — filled as each lands.

- [ ] **Per-agent sandbox enforcement** — namespaced network egress fence (nft / TPROXY),
      minimal per-agent root filesystems, resource-limit wiring.
- [ ] **Control plane & domain model** — DAG / issue / project state, scheduler,
      persistence.
- [ ] **Configuration files** — role cards and per-agent policy from disk.
- [ ] **`plexus inspect`** — mesh/bus diagnostics.
- [ ] **Deployment guide** — external NATS, multi-agent clusters.

## License

> **TODO.** No license has been declared yet.
