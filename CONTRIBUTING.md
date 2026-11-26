# Contributing to SmallCloud

Thank you for contributing to SmallCloud! Please follow the setup, coding standards, and verification steps below before opening a pull request.

## Development Setup

Ensure the following toolchains are installed locally:

- **Go**: 1.23+
- **Python**: 3.12+ (for `templates/fastapi-sqlite/`)
- **Node.js**: 22+ (for `templates/node-sqlite/`)

Clone the repository and verify the local toolchain:

```bash
git clone https://github.com/creova-gif/smallcloud.git
cd smallcloud
go test -race ./...
```

## Coding Conventions

- **Comment-Free Source Code**: Keep Go, Python, and JavaScript source files clean and self-documenting without inline comments.
- **Formatting**: Run `gofmt -w .` on all Go changes.
- **Error Handling**: Always check and handle returned errors explicitly in Go; never swallow exceptions silently in Python.
- **Architecture Decisions**: Record any non-trivial design choice in `docs/adr/` using `docs/adr/000-template.md`.
- **Security**: Never commit `.env` files, API keys, webhook secrets, or PII/PHI fixtures.

## Verification Checklist

Run the full CI verification suite locally before submitting a PR:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
python3 -m unittest -v templates/fastapi-sqlite/test_app.py
node --test templates/node-sqlite/test_server.mjs
go run ./cli/cmd/smallcloud eval
```

## Pull Request Expectations

1. Keep changes focused and include deterministic unit tests for new behavior or bug fixes.
2. Complete the [pull request template](.github/pull_request_template.md).
3. Ensure all CI checks (`gofmt`, `go vet`, `go test -race`, template tests, and `smallcloud eval`) pass.
4. Update `README.md`, `CHANGELOG.md`, or `docs/adr/` when adding user-facing flags, commands, or architectural changes.
