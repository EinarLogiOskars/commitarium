# ADR-001: Use Go for the workflow coordinator

- Status: Accepted
- Date: 05-09-2026

## Context

Commitarium is a desktop application made to coordinate and orchestrate different coding agents, get them to work together as a team to return a single result for the user to review. It will be a GUI built on top of a desktop-side host controller that starts docker and runs a Dockerized setup with a workflow coordinator, agent workers, a Forgejo instance, and isolated CI/test workers. The container workflow coordinator runs inside Compose.

The application will initially support Codex and Claude Code, but will be implemented in a way that other providers should be straightforward to add.

## Decision

Commitarium will use Go for the containerized workflow coordinator and agent-worker supervisor services.

Go is not being selected because TypeScript or Java are incapable of supporting the system. Both are viable alternatives. Go is being selected because it supports the required process supervision, streaming, concurrency, and cancellation patterns while also serving an explicit project goal: learning a new systems-oriented language through a substantial application.

The additional development time and mistakes associated with learning Go are accepted costs of this decision.

## Consequences

### Positive

- Go provides language-level concurrency suitable for multiple active workflows.
- Contexts provide a consistent model for deadlines and cancellation.
- The standard library supports HTTP, JSON, testing, and process execution.
- Services compile into native binaries without requiring a separate runtime.
- Go supports the project’s explicit learning and portfolio goals.

### Negative

- Initial learning curve and slower delivery
- Greater likelihood of early refactoring
- Need to generate TypeScript clients from API contracts
- Another language in the overall technology stack

## Alternatives considered

### TypeScript and Node.js

Technically suitable and familiar, but the maintainer does not want TypeScript as the orchestration backend. Sharing frontend types was not considered enough reason to choose it.

### Java and Spring Boot

Technically suitable and probably the lowest-risk delivery option because of existing experience. Not chosen because this project is also intended to expand the maintainer’s systems-programming experience.

## Reconsideration criteria

Reconsider the choice if the early process-supervision implementation shows
that Go materially obstructs required functionality or creates unreasonable
maintenance complexity.
