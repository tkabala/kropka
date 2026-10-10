# Contributing

Thanks for helping out. Bug reports, fixes and small focused features are welcome.
For anything larger, open an issue first so we can agree on the approach.

## Development

You need Go (see `go.mod`); `ffmpeg` is optional and only used by the video thumbnail tests.
The UI's lint and browser tests also need Biome and Node (`mise install` gets all of them).

```sh
make build   # bin/kropka
make test    # go test -race ./...
make vet
make lint    # golangci-lint and Biome
make e2e     # Playwright tests of the UI
```

`ARCHITECTURE.md` explains how the pieces fit together.

## Pull requests

- Keep each PR to one change, with tests where it makes sense.
- Make sure `make test vet lint` passes, and `make e2e` for UI changes; CI runs the Go tests
  on Linux, macOS and Windows.
- Write commit messages in the existing style: `fix: …`, `feat: …`, `docs: …`, `ci: …`.

By contributing you agree that your work is released under the [MIT License](LICENSE).
