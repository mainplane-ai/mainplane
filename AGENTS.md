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
- GitHub issues close on merge via "Fixes #n" in the PR description. For an outside reporter, also leave a one-line thanks naming the PR.
- main takes no outside PRs. An outside reporter gets credit instead: when a merged PR uses their issue, its repro, or their branch, the squash commit ends with `Co-authored-by: <login> <<id>+<login>@users.noreply.github.com>` for each of them (id from `gh api users/<login> --jq .id`). GitHub then lists them as contributors. This is how we grow the community, so do not skip it.
- PR titles and descriptions are public and permanent. Write them for a reader who has only this repo: what changed and why, in the project's own terms. Leave out how the work was organized: people, sessions, agents, private docs and trackers, and the names of our machines. Name a test machine by what it is, such as a fresh Ubuntu 26.04 VM or Windows 11.
