# VOICEBLENDER points to the VoiceBlender repository root.
# Override on the command line: make generate VOICEBLENDER=/other/path
VOICEBLENDER ?= ../VoiceBlender
OPENAPI      := $(VOICEBLENDER)/openapi.yaml
ASYNCAPI     := $(VOICEBLENDER)/asyncapi.yaml

.PHONY: generate build vet

# generate reads openapi.yaml + asyncapi.yaml and rewrites the generated files
# (models.go, requests.go, responses.go, events.go, legs.go, rooms.go,
# webrtc.go, vsi.go). Run this whenever either spec changes.
generate:
	cd cmd/generate && go mod tidy && go run . \
		-openapi $(abspath $(OPENAPI)) \
		-asyncapi $(abspath $(ASYNCAPI)) \
		-out $(abspath .)
	go fmt ./...
	$(MAKE) vet

build:
	go build ./...

vet:
	go build ./...
	go vet ./...
