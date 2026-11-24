BINDIR ?= $(HOME)/bin

.PHONY: install
install:
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/jiggle-go ./cmd/jiggle-go
