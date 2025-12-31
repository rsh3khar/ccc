.PHONY: build install clean

build:
	go build -o ccc
	@if [ "$$(uname)" = "Darwin" ]; then \
		codesign -s - ccc 2>/dev/null || true; \
	fi

install: build
	mkdir -p ~/bin
	cp ccc ~/bin/
	@echo "✅ Installed to ~/bin/ccc"

clean:
	rm -f ccc
