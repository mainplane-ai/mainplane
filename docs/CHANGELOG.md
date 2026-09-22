# Changelog

One section per release, newest first. An `-rc` tag ships the section of the release it is a candidate for.

## v0.1.0

The first release: a harness, workers that dial it, and a CLI.

- `mainplane-server up` runs the harness: sessions as append-only state files, fork and revert by copy, stop and retry, context limits, and five provider envelopes (Anthropic, OpenAI Responses, OpenAI Chat compatible, Gemini, Bedrock)
- `mainplane-server key` and `join` issue api keys for connectors and join tokens for workers; the table holds only hashes
- `mainplane install <join token>` makes a machine a worker at every boot: systemd on Linux, a LaunchDaemon on macOS, a logon task on Windows
- `mainplane login <api key>` and one CLI verb per harness route: `new`, `message`, `tail`, `chat`, `retry`, `stop`, `info`, `sessions`, `workers`, `providers`
- Every binary carries its release version (`mainplane version`, `mainplane-server version`); the harness refuses a worker from another release and says which to install
