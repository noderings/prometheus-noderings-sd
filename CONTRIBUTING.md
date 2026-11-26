# Contributing

Thanks for helping improve prometheus-noderings-sd.

1. Open an issue describing the bug or change before large pull requests.
2. Fork the repository and create a topic branch.
3. Run `make test` and `make lint`; both must pass. Unit tests use fakes and need no token.
4. Add tests for new behaviour. When you change API calls or labels, also run
   `NODERINGS_TOKEN=... make e2e`. It only reads from the API and runs a real Prometheus
   in Docker to check that every VM is discovered.
5. New labels must start with `__meta_noderings_`, use `snake_case`, and be listed in the
   README. Do not rename or remove labels in a minor release: users' relabel rules depend on them.
6. Use [Conventional Commits](https://www.conventionalcommits.org/) and sign off each commit
   (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).

Please report security problems privately as described in [SECURITY.md](SECURITY.md).
