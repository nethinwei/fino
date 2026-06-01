# Security Policy

## Supported versions

fino is pre-1.0. Security fixes are applied to the latest tagged release and
the `main` branch. Please make sure you can reproduce an issue against the
latest version before reporting.

## Reporting a vulnerability

Please **do not** open a public issue for security vulnerabilities.

Instead, use GitHub's private vulnerability reporting: go to the repository's
**Security** tab → **Report a vulnerability**
(<https://github.com/nethinwei/fino/security/advisories/new>).

Include enough detail to reproduce: affected version/commit, a minimal
proof-of-concept, and the impact you observed. We aim to acknowledge reports
within a few days and will coordinate a fix and disclosure timeline with you.

## Scope

fino is a library: it executes the ReAct loop, adapts LLM provider APIs, and
runs the tools you give it. Tool side effects, authorization semantics, secret
handling, and deployment are the responsibility of the integrating application
(see `docs/design.md`). Reports about how the SDK itself handles untrusted
model output, request construction, or transport are in scope.
