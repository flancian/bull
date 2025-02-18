.PHONY: run test

all: install

test:
	CGO_ENABLED=0 go test -fullpath ./...
	CGO_ENABLED=0 go tool staticcheck ./...

install: test
	CGO_ENABLED=0 go install ./cmd/...

run: install
	sh -c ' \
	bull serve --bull_static=internal/assets/ & \
	bull --content=$$HOME/hugo/content serve --bull_static=internal/assets/ --listen=localhost:4444 & \
	bull --content ~/keep serve --bull_static=internal/assets/ --listen=localhost:5555 --editor=textarea & \
	wait \
	'
