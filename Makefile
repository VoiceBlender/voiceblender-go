# VOICEBLENDER points to the VoiceBlender repository root.
# Override on the command line: make generate VOICEBLENDER=/other/path
VOICEBLENDER ?= ../VoiceBlender
OPENAPI      := $(VOICEBLENDER)/openapi.yaml

.PHONY: generate build vet

# generate reads openapi.yaml and rewrites models.go, requests.go, responses.go.
# Run this whenever openapi.yaml changes.
generate:
	cd cmd/generate && go mod tidy && go run . \
		-openapi $(abspath $(OPENAPI)) \
		-out $(abspath .)
	go fmt ./...
	$(MAKE) vet

build:
	go build ./...

vet:
	go build ./...
	go vet ./...
