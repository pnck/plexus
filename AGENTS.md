# Plexus Architecture & Development Guidelines

**CRITICAL INSTRUCTIONS FOR AI AGENTS:**

This file contains the foundational architectural decisions and rules for the `plexus` project. If you are an AI agent generating code for this repository, you MUST adhere to these rules strictly.

## 1. Project Essence
Plexus is a high-density, bubblewrap-sandboxed agent execution engine with mesh communication. It operates as a single, self-contained executable.

## 2. Core Technology Stack
- **Language**: Go (Golang).
- **Communication Bus**: NATS (TCP + mTLS). We embed `nats-server` directly into the Go binary. We DO NOT rely on external message brokers.
- **Message Abstraction**: Watermill library is used to abstract pub/sub mechanics.
- **Sandboxing Engine**: Bubblewrap (`bwrap`).

## 3. Sandboxing & The Self-Reexec Pattern
- **DO NOT** attempt to use `chroot` natively or import `libcontainer`/Docker APIs.
- We utilize the **Self-Reexec** pattern. The binary is designed to execute itself under `bwrap` to spawn isolated sub-agents.
- `bwrap` handles the Mount Namespace isolation (mapping independent `/workspace` and config directories per agent) while sharing the read-only host OS layers to maximize density.
- We aim to embed the statically compiled `bwrap` binary directly using `//go:embed` to achieve a pure single-file distribution.

## 4. Coding Conventions
- Favor standard library capabilities where possible.
- Avoid introducing heavy external dependencies unless absolutely necessary.
- Ensure all NATS and Watermill integrations are gracefully cancellable via `context.Context`.
