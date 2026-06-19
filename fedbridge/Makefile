BINARY = main
TAGS = gomuks,toxoverhttp,emailimap,matrixlite

all:
	go build -v -tags $(TAGS) -o $(BINARY)
	@case "$$(uname -s)" in \
		Linux) \
			echo "==> setcap cap_net_admin+eip on $(BINARY)"; \
			sudo setcap cap_net_admin+eip $(BINARY);; \
		Darwin) \
			echo "==> macOS: run with sudo for utun device";; \
	esac

# still not done for need ndk
a64:
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 go build -v -tags $(TAGS) -o $(BINARY).arm64
