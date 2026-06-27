BIN := gh-copilot-instructions

# Build the local binary. With a dev (symlink) install in place, this is the
# whole iteration loop: `make` then re-run `gh copilot-instructions ...`.
build:
	go build -o $(BIN) .

# Point the installed gh extension at this working copy (symlink install), so
# `gh copilot-instructions ...` runs your local build with no tag or release.
# Run once; afterwards just `make` to rebuild.
dev: build
	-gh extension remove $(BIN) 2>/dev/null
	gh extension install .

# Switch back to the published release build.
release:
	-gh extension remove $(BIN) 2>/dev/null
	gh extension install laserlemon/$(BIN)

test:
	go test ./...

.PHONY: build dev release test
