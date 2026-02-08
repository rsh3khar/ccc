#!/bin/bash
# Setup script for CCC on Raspberry Pi 5
# Run this on the Pi after cloning the repo
set -euo pipefail

echo "=== CCC Pi Setup ==="
echo ""

# Step 1: Check/Install Go
if command -v go &>/dev/null; then
    GO_VERSION=$(go version | grep -oP 'go\K[0-9.]+')
    echo "Go $GO_VERSION already installed"
else
    echo "Installing Go 1.24 for ARM64..."
    GO_TAR="go1.24.0.linux-arm64.tar.gz"
    curl -LO "https://go.dev/dl/$GO_TAR"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "$GO_TAR"
    rm "$GO_TAR"

    # Add to PATH if not already there
    if ! grep -q '/usr/local/go/bin' ~/.profile 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.profile
    fi
    export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin

    echo "Go $(go version) installed"
fi

# Step 2: Build CCC
echo ""
echo "Building CCC..."
cd "$(dirname "$0")/.."
CGO_ENABLED=0 go build -o ccc
echo "Built successfully"

# Step 3: Install binary
echo ""
echo "Installing to ~/.local/bin/ccc..."
mkdir -p ~/.local/bin
install -m 755 ccc ~/.local/bin/ccc

# Add to PATH if not already there
if ! grep -q '\.local/bin' ~/.profile 2>/dev/null; then
    echo 'export PATH=$PATH:$HOME/.local/bin' >> ~/.profile
fi
export PATH=$PATH:$HOME/.local/bin

echo "Installed"

# Step 4: Check dependencies
echo ""
echo "Checking dependencies..."
echo -n "  tmux: "
if command -v tmux &>/dev/null; then
    echo "$(tmux -V)"
else
    echo "NOT FOUND - install with: sudo apt install tmux"
    exit 1
fi

echo -n "  claude: "
if command -v claude &>/dev/null; then
    echo "found"
else
    echo "NOT FOUND - install with: npm install -g @anthropic-ai/claude-code"
    exit 1
fi

echo ""
echo "=== Setup complete ==="
echo ""
echo "Next steps:"
echo "  1. Run: ccc setup <your-bot-token>"
echo "  2. Configure OpenRouter: ccc config openrouter-key <your-key>"
echo "  3. The setup command will install the systemd service automatically"
echo ""
echo "Or run 'ccc doctor' to verify everything is configured."
