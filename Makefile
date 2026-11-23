# For the Go half of jiggle.  Perl jiggle is built by Dist::Zilla; see dist.ini.

BINDIR ?= $(HOME)/bin

.PHONY: install
install:
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/jiggle-go ./cmd/jiggle-go
