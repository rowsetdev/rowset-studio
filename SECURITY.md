# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately through GitHub's
[Security Advisories](https://github.com/rowsetdev/rowset-studio/security/advisories/new)
for this repository ("Report a vulnerability" under the Security tab), rather
than opening a public issue.

Include what you can: the affected version (`rowset --version`), the engine
or feature involved, reproduction steps, and the potential impact. We'll
acknowledge the report and follow up as we investigate.

## Supported versions

Only the latest release is supported. Please update (`rowset` or `rowset
desktop-stop && rowset`, or rerun the install script) before reporting an
issue, to confirm it isn't already fixed.

## Scope

Rowset Studio runs locally: the server binds to loopback, credentials are
encrypted in a local SQLite database, and there's no multi-tenant cloud
service in scope. Reports about the desktop app, the local server/API, the
installer scripts, and the guardrail/policy engine are all welcome.
