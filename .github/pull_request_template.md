## Summary
Describe the changes in this pull request and link any related issues or ADRs.

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Documentation / templates
- [ ] Refactor / chore

## Verification Checklist
- [ ] `test -z "$(gofmt -l .)"` passes
- [ ] `go vet ./...` passes
- [ ] `go test -race ./...` passes
- [ ] `python3 -m unittest -v templates/fastapi-sqlite/test_app.py` passes
- [ ] `node --test templates/node-sqlite/test_server.mjs` passes
- [ ] `go run ./cli/cmd/smallcloud eval` passes
- [ ] No `.env` files, API keys, or secrets are included
