FLUTTER_VERSION ?= 3.38.5
IMAGE           ?= mobaiapp/iosbox
TAG             ?= flutter-$(FLUTTER_VERSION)
PLATFORM        ?= linux/amd64

.PHONY: build push run-setup run-build clean

build:
	docker build \
		--platform $(PLATFORM) \
		--build-arg FLUTTER_VERSION=$(FLUTTER_VERSION) \
		-t $(IMAGE):$(TAG) \
		-t $(IMAGE):latest \
		.

push:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg FLUTTER_VERSION=$(FLUTTER_VERSION) \
		--tag $(IMAGE):$(TAG) \
		--tag $(IMAGE):latest \
		--push \
		.

# One-time SDK setup: make run-setup XCODE=/path/to/Xcode.xip
run-setup:
	docker run --rm --platform linux/amd64 \
		-v $(XCODE):/workspace/Xcode.xip \
		-v iosbox-sdk:/root/.iosbox \
		$(IMAGE):$(TAG) \
		iosbox setup /workspace/Xcode.xip

# Build a Flutter project: make run-build PROJECT=/path/to/app [RELEASE=1]
run-build:
	docker run --rm --platform linux/amd64 \
		-v iosbox-sdk:/root/.iosbox \
		-v $(PROJECT):/project \
		$(IMAGE):$(TAG) \
		iosbox build /project

clean:
	docker rmi $(IMAGE):$(TAG) $(IMAGE):latest 2>/dev/null || true
