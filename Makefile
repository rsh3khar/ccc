.PHONY: build install clean

UNAME := $(shell uname)

build:
	CGO_ENABLED=0 go build -o ccc

install: build
	mkdir -p ~/.local/bin
	install -m 755 ccc ~/.local/bin/ccc
	@echo "Installed to ~/.local/bin/ccc"

clean:
	rm -f ccc

# Cross-compile for Pi (ARM64 Linux)
build-pi:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ccc-linux-arm64
