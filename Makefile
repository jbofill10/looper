BINARY := looper
INSTALL_DIR := /usr/local/bin

.PHONY: build install test vet fmt clean

build:
	go build -o $(BINARY) .

install: build
	sudo mkdir -p $(INSTALL_DIR)
	sudo cp $(BINARY) $(INSTALL_DIR)/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

clean:
	rm -f $(BINARY)
