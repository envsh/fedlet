all:
	go build -v -tags gomuks,toxoverhttp,emailimap,matrixlite

# still not done for need ndk
a64:
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 go build -v -tags gomuks,toxoverhttp,emailimap,matrixlite -o main.arm64
