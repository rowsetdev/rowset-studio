# Contributing

Rowset Studio is source-available under the
[PolyForm Noncommercial License 1.0.0](LICENSE): reading, forking and
modifying the source is open, and issues and pull requests are welcome.

## Before you start

- For a bug, check [open issues](https://github.com/rowsetdev/rowset-studio/issues)
  first; include the engine, OS, version (`rowset --version`) and steps to
  reproduce.
- For a new feature or a larger change, open an issue to discuss the
  approach before writing code — it saves rework on both sides.
- Small, focused fixes (typos, docs, a clear bug) can go straight to a pull
  request.

## Development setup

See [Build requirements](README.md#build-requirements) and
[Development](README.md#development) in the README for the toolchain,
directory layout and the commands below.

```sh
(cd rowset-core && go vet ./... && go test ./...)
(cd rowset-studio && npm ci && npm run build && npm run lint && npm test)
```

Live engine tests (real database containers) and browser E2E tests are
opt-in and documented in the README's Development section; they aren't
required for most pull requests.

## Pull requests

- Keep changes focused; unrelated cleanup makes review harder.
- Match the existing code style (no new comments beyond what the code
  actually needs, no speculative abstractions).
- Update [CHANGELOG.md](CHANGELOG.md) and bump the patch version in
  `rowset-studio/package.json` for any user-visible change, per the
  convention already in the changelog's own header.
- Make sure the commands above pass before opening the PR; CI runs the same
  checks plus a Windows cross-compile and isolated live-engine jobs.

## Code of Conduct

This project follows a [Code of Conduct](CODE_OF_CONDUCT.md). By
participating, you're expected to uphold it.
