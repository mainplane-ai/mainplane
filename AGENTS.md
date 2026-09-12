# Go rules

- One owner per fact. A name, a constant, a shape lives in one place and is read from there.
- A failure the model should read is a result. A failure the harness should handle is an error. Never both, never swallowed.
- When an upstream shape is uncertain, keep the bytes. Raw event in the error, raw block in the record. Guessed fields lose information.
- Constants sit at the top of the file with the reason for the value, not the name of the value.
- A helper earns its place at the third copy inside one package. Never before, never across packages for six lines.

# Github info
- main takes PRs only, squash merged.
- Cubic AI reviews every push to a PR and merge waits for it
- working_dir/ is gitignored and excluded from lint