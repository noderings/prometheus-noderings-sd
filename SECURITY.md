# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems. Email **partners@noderings.com** with:

- a description of the issue and its impact
- steps to reproduce, or a proof of concept
- the version (`prometheus-noderings-sd --version`)

We acknowledge reports within three working days and keep you updated until the issue is
fixed. Credit is given in the release notes unless you ask us not to.

## Supported versions

Security fixes are made for the latest minor release.

## Handling credentials

The token is read from `NODERINGS_TOKEN` or from the file in `--noderings.token-file`, never
from a command-line flag, so it does not appear in process lists. It is never logged. The
service only reads the VM list; use a service account token with read access only.

The `/targets` response lists your VMs' IP addresses. Keep the service on a private network
or protect it with `--web.config.file` (TLS and basic auth).
