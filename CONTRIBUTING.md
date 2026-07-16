# Contributing to k8s-dashboard

Thanks for your interest — contributions are welcome.

## Reporting bugs

Open an issue using the bug template. Include:
- What you saw vs. what you expected
- Steps to reproduce
- Environment (image tag, K8s version, OIDC provider if any)
- Logs / screenshots if useful

## Proposing features

Open a feature-request issue before writing code. This avoids wasted effort if the shape isn't what fits the project.

## Sending a PR

1. Fork + branch: `git checkout -b feat/short-name`
2. Small, focused commits — read `git log` for the tone of the messages
3. `gofmt -s -w .` before pushing
4. `go build ./... && go test ./... && go vet ./...` all green
5. Open a PR against `main` with:
   - What the change does and **why**
   - Link to the tracking issue if any
   - Screenshots for UI changes
   - Rationale for anything security-sensitive (auth, RBAC, SMTP, session store)

## Areas where help is especially welcome

- Test coverage — the current suite is minimal
- Log viewer robustness — the owner-ref lookup fix (see Roadmap in README)
- Multi-cluster support
- Mobile client (Flutter or native Android)
- Grafana panel embedding per product

## Code style

- Go: idiomatic; `gofmt -s`; error-wrap with `fmt.Errorf("...: %w", err)`
- Frontend: no build step. Vanilla HTML/CSS/JS in `web/`. Keep it dependency-free.
- Config: `config.yaml` for declarative, env vars for secrets and per-env overrides. No hard-coded values in code.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
