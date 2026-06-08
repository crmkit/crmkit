# Changelog

All notable changes to crmkit, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

## [0.3.0] - 2026-06-08

### Features

- Ticket management: full CRUD, ticket activities with conversation summaries, and follow-ups
- Extend cross-entity search to include tickets
- Stricter email validation on contact and auth flows

## [0.2.0] - 2026-06-07

### Features

- Implement optimistic concurrency control with versioning for contacts, companies, and deals
- Implement detailed audit logging with filtering by target and record history
- Update confirmation messages to use stable internal IDs for deletions

## [0.1.11] - 2026-06-07

### Features

- Introduce short, workspace-scoped public handles for CRM records

## [0.1.10] - 2026-06-07

### Features

- Add tags support for companies and contacts, including filtering
- Implement custom-field filtering for companies and deals
- Harden query parsing against SQL injection (cursor validation + safety coverage)
- Enhance company management with notes and activity logging
- Implement activity deletion and activity-quota management
- Implement audit-log retention with pruning

## [0.1.9] - 2026-06-06

The initial public line of crmkit - an agent-first, headless CRM driven over a
plain-text HTTP API, with an MCP connector for chat clients.

### Features

- Core CRM: contacts, companies, deals, and activities, all workspace-scoped
- Cross-entity search across contacts, companies, and deals
- Per-workspace timezone with localized date display
- `created_by` attribution and activity summaries on records, with filtering by creator
- Audit entries attributed to the acting member (human or agent)
- Create a workspace during the OAuth authorization flow
- OTP auth with bearer tokens, MCP connector, and per-plan resource limits
