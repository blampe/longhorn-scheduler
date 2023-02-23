PROJECT ?= longhorn-scheduler
REGISTRY ?= blampe
PLATFORMS ?= linux/amd64,linux/arm64
VERSION ?= $(shell git describe --tags --match "v*.*" HEAD)
TAG ?= $(VERSION)
NOCACHE ?= false

help:
	@echo "Useful targets: 'update', 'upload'"

all: update upload

.PHONY: update
update:
	docker buildx build $(_EXTRA_ARGS) \
		--build-arg=VERSION=$(VERSION) \
		--platform=$(PLATFORMS) \
		--no-cache=$(NOCACHE) \
		--pull=$(NOCACHE) \
		--tag $(REGISTRY)/$(PROJECT):$(TAG) \
		--tag $(REGISTRY)/$(PROJECT):latest \
		. ;\

.PHONY: upload
upload:
	make update _EXTRA_ARGS=--push
