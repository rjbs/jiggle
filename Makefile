BINDIR ?= $(HOME)/bin

.PHONY: install test
install:
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/jiggle-go ./cmd/jiggle-go

# The tests skip what they haven't the programs for: vips, ffmpeg, exiftool
# and node.  Those that drive the editor in a browser run only with
# JIGGLE_PLAYWRIGHT set to a directory where Playwright is installed.
test:
	go vet ./...
	go test ./...
