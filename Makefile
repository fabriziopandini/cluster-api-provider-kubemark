# Copyright 2018 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# If you update this file, please follow:
# https://suva.sh/posts/well-documented-makefiles/

.DEFAULT_GOAL:=help

# Use GOPROXY environment variable if set
GOPROXY := $(shell go env GOPROXY)
ifeq ($(GOPROXY),)
GOPROXY := https://proxy.golang.org
endif
export GOPROXY

# Active module mode, as we use go modules to manage dependencies
export GO111MODULE=on

# This option is for running docker manifest command
export DOCKER_CLI_EXPERIMENTAL := enabled

# Directories.
ROOT_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
TOOLS_DIR := hack/tools
TOOLS_BIN_DIR := $(TOOLS_DIR)/bin
TEST_DIR := test
BIN_DIR := bin

# Binaries.
CONTROLLER_GEN := $(TOOLS_BIN_DIR)/controller-gen
CONVERSION_GEN := $(TOOLS_BIN_DIR)/conversion-gen
GOLANGCI_LINT := $(TOOLS_BIN_DIR)/golangci-lint
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
KUSTOMIZE := $(TOOLS_BIN_DIR)/kustomize

# Define Docker related variables. Releases should modify and double check these vars.
REGISTRY ?= gcr.io/$(shell gcloud config get-value project)
STAGING_REGISTRY := gcr.io/cf-london-servces-k8s/bmo/cluster-api-kubemark-staging
PROD_REGISTRY ?= gcr.io/cf-london-servces-k8s/bmo/cluster-api-kubemark
IMAGE_NAME ?= cluster-api-kubemark-controller
CONTROLLER_IMG ?= $(REGISTRY)/$(IMAGE_NAME)
TAG ?= dev
ARCH ?= amd64
ALL_ARCH = amd64 arm arm64

#
# Ginkgo configuration.
#
GINKGO_FOCUS ?=
GINKGO_SKIP ?=
GINKGO_NODES ?= 1
GINKGO_TIMEOUT ?= 2h
GINKGO_POLL_PROGRESS_AFTER ?= 10m
GINKGO_POLL_PROGRESS_INTERVAL ?= 1m
E2E_CONF_FILE ?= $(ROOT_DIR)/$(TEST_DIR)/e2e/config/kubemark.yaml
SKIP_RESOURCE_CLEANUP ?= false
USE_EXISTING_CLUSTER ?= false
GINKGO_NOCOLOR ?= false

# to set multiple ginkgo skip flags, if any
ifneq ($(strip $(GINKGO_SKIP)),)
_SKIP_ARGS := $(foreach arg,$(strip $(GINKGO_SKIP)),-skip="$(arg)")
endif

# Allow overriding the imagePullPolicy
PULL_POLICY ?= Always

all: test manager clusterctl

help:  ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

## --------------------------------------
## Testing
## --------------------------------------

ARTIFACTS ?= ${ROOT_DIR}/_artifacts

.PHONY: test
test: ## Run tests
	go test -v ./...

.PHONY: test-e2e
test-e2e: ## Launch integration e2e test with building images (for local usage)
	$(MAKE) -C hack/tests test-e2e

.PHONY: test-e2e-v2
test-e2e-v2: $(GINKGO) ## Run the end-to-end tests
	$(GINKGO) -v --trace -poll-progress-after=$(GINKGO_POLL_PROGRESS_AFTER) \
		-poll-progress-interval=$(GINKGO_POLL_PROGRESS_INTERVAL) --tags=e2e --focus="$(GINKGO_FOCUS)" \
		$(_SKIP_ARGS) --nodes=$(GINKGO_NODES) --timeout=$(GINKGO_TIMEOUT) --no-color=$(GINKGO_NOCOLOR) \
		--output-dir="$(ARTIFACTS)" --junit-report="junit.e2e_suite.1.xml" $(GINKGO_ARGS) $(ROOT_DIR)/$(TEST_DIR)/e2e -- \
		-e2e.artifacts-folder="$(ARTIFACTS)" \
		-e2e.config="$(E2E_CONF_FILE)" \
		-e2e.skip-resource-cleanup=$(SKIP_RESOURCE_CLEANUP) -e2e.use-existing-cluster=$(USE_EXISTING_CLUSTER)

## --------------------------------------
## Binaries
## --------------------------------------

.PHONY: manager
manager: ## Build manager binary
	go build -o $(BIN_DIR)/manager github.com/kubernetes-sigs/cluster-api-provider-kubemark

$(CONTROLLER_GEN): $(TOOLS_DIR)/go.mod # Build controller-gen from tools folder.
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/controller-gen sigs.k8s.io/controller-tools/cmd/controller-gen

$(CONVERSION_GEN): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/conversion-gen k8s.io/code-generator/cmd/conversion-gen

$(GOLANGCI_LINT): $(TOOLS_DIR)/go.mod # Build golangci-lint from tools folder.
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/golangci-lint github.com/golangci/golangci-lint/cmd/golangci-lint

$(GINKGO): $(TOOLS_DIR)/go.mod # Build controller-gen from tools folder.
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/ginkgo github.com/onsi/ginkgo/v2/ginkgo

$(KUSTOMIZE): # Build kustomize from tools folder.
	hack/ensure-kustomize.sh
## --------------------------------------
## Linting
## --------------------------------------

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Lint codebase
	$(GOLANGCI_LINT) run -v --timeout=5m $(GOLANGCI_LINT_EXTRA_ARGS)

.PHONY: lint-fix
lint-fix: $(GOLANGCI_LINT) ## Lint the codebase and run auto-fixers if supported by the linter
	GOLANGCI_LINT_EXTRA_ARGS=--fix $(MAKE) lint

.PHONY: lint-full
lint-full: $(GOLANGCI_LINT) ## Run slower linters to detect possible issues
	GOLANGCI_LINT_EXTRA_ARGS=--fast=false $(MAKE) lint

## --------------------------------------
## Generate / Manifests
## --------------------------------------

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code
	$(MAKE) generate-manifests
	$(MAKE) generate-go

.PHONY: generate-go
generate-go: $(CONTROLLER_GEN) $(CONVERSION_GEN) ## Runs Go related generate targets
	$(CONTROLLER_GEN) \
		object:headerFile=./hack/boilerplate/boilerplate.generatego.txt \
		paths=./api/...
	$(CONVERSION_GEN) \
		--input-dirs=./api/v1alpha4 \
		--build-tag=ignore_autogenerated_capk_v1alpha4 \
		--extra-peer-dirs=sigs.k8s.io/cluster-api/api/v1beta1 \
		--output-file-base=zz_generated.conversion \
		--go-header-file=./hack/boilerplate/boilerplate.generatego.txt

.PHONY: generate-manifests
generate-manifests: $(CONTROLLER_GEN) ## Generate manifests e.g. CRD, RBAC etc.
	$(CONTROLLER_GEN) \
		paths=./api/... \
		paths=./controllers/... \
		crd:crdVersions=v1 \
		rbac:roleName=manager-role \
		output:crd:dir=./config/crd/bases \
		output:webhook:dir=./config/webhook \
		webhook

.PHONY: modules
modules: ## Runs go mod to ensure modules are up to date.
	go mod tidy
	cd $(TOOLS_DIR); go mod tidy
	cd $(TEST_DIR); go mod tidy

## --------------------------------------
## Docker
## --------------------------------------

.PHONY: docker-build
docker-build: ## Build the docker image for controller-manager
	DOCKER_BUILDKIT=1 docker build --pull --build-arg goproxy=$(GOPROXY) --build-arg ARCH=$(ARCH) . -t $(CONTROLLER_IMG)-$(ARCH):$(TAG) --file Dockerfile
	MANIFEST_IMG=$(CONTROLLER_IMG)-$(ARCH) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: docker-build-e2e
docker-build-e2e: ## Run docker-build with settings to be used for the e2e tests
	# Note: The generated image name must match the image names used in the E2E_CONF_FILE
	$(MAKE) REGISTRY=gcr.io/k8s-staging-cluster-api PULL_POLICY=IfNotPresent TAG=dev docker-build

.PHONY: docker-push
docker-push: ## Push the docker image
	docker push $(CONTROLLER_IMG)-$(ARCH):$(TAG)

## --------------------------------------
## Docker — All ARCH
## --------------------------------------

.PHONY: docker-build-all ## Build all the architecture docker images
docker-build-all: $(addprefix docker-build-,$(ALL_ARCH))

docker-build-%:
	$(MAKE) ARCH=$* docker-build

.PHONY: docker-push-all ## Push all the architecture docker images
docker-push-all: $(addprefix docker-push-,$(ALL_ARCH))
	$(MAKE) docker-push-manifest

docker-push-%:
	$(MAKE) ARCH=$* docker-push

.PHONY: docker-push-manifest
docker-push-manifest: ## Push the fat manifest docker image.
	## Minimum docker version 18.06.0 is required for creating and pushing manifest images.
	docker manifest create --amend $(CONTROLLER_IMG):$(TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(CONTROLLER_IMG)\-&:$(TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${CONTROLLER_IMG}:${TAG} ${CONTROLLER_IMG}-$${arch}:${TAG}; done
	docker manifest push --purge $(CONTROLLER_IMG):$(TAG)
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: set-manifest-image
set-manifest-image:
	$(info Updating kustomize image patch file for manager resource)
	sed -i'' -e 's@image: .*@image: '"${MANIFEST_IMG}:$(MANIFEST_TAG)"'@' ./config/default/manager_image_patch.yaml

.PHONY: set-manifest-pull-policy
set-manifest-pull-policy:
	$(info Updating kustomize pull policy file for manager resource)
	sed -i'' -e 's@imagePullPolicy: .*@imagePullPolicy: '"$(PULL_POLICY)"'@' ./config/default/manager_pull_policy.yaml

## --------------------------------------
## Release
## --------------------------------------

GIT_TAG := $(shell git describe --abbrev=0 2>/dev/null)
RELEASE_TAG := $(lastword $(subst /, ,$(GIT_TAG)))
RELEASE_DIR := out

$(RELEASE_DIR):
	mkdir -p $(RELEASE_DIR)/

.PHONY: release
release: clean-release ## Builds and push container images using the latest git tag for the commit.
	@if [ -z "${RELEASE_TAG}" ]; then echo "RELEASE_TAG is not set"; exit 1; fi
	@if ! [ -z "$$(git status --porcelain)" ]; then echo "Your local git repository contains uncommitted changes, use git clean before proceeding."; exit 1; fi
	git checkout "${GIT_TAG}"
	# Set the manifest image to the staging bucket.
	MANIFEST_IMG=$(PROD_REGISTRY)/$(IMAGE_NAME) MANIFEST_TAG=$(RELEASE_TAG) \
		$(MAKE) set-manifest-image
	PULL_POLICY=IfNotPresent $(MAKE) set-manifest-pull-policy
	$(MAKE) release-manifests
	$(MAKE) release-templates
	REGISTRY=$(PROD_REGISTRY) $(MAKE) docker-build-all docker-push-all release-alias-tag
	# Add metadata to the release artifacts
	cp metadata.yaml $(RELEASE_DIR)/metadata.yaml

.PHONY: release-manifests
release-manifests: $(KUSTOMIZE) $(RELEASE_DIR) ## Builds the manifests to publish with a release
	$(KUSTOMIZE) build config/ > $(RELEASE_DIR)/infrastructure-components.yaml

.PHONY: release-staging
release-staging: ## Builds and push container images to the staging bucket.
	docker pull docker.io/docker/dockerfile:experimental
	docker pull docker.io/library/golang:1.15.3
	docker pull gcr.io/distroless/static:latest
	REGISTRY=$(STAGING_REGISTRY) $(MAKE) docker-build-all docker-push-all release-alias-tag

.PHONY: release-templates
release-templates: $(RELEASE_DIR)
	cp templates/cluster-template*.yaml $(RELEASE_DIR)/

RELEASE_ALIAS_TAG=$(PULL_BASE_REF)

.PHONY: release-alias-tag
release-alias-tag: # Adds the tag to the last build tag.
	gcloud container images add-tag $(CONTROLLER_IMG):$(TAG) $(CONTROLLER_IMG):$(RELEASE_ALIAS_TAG)

## --------------------------------------
## Cleanup / Verification
## --------------------------------------

.PHONY: clean
clean: ## Remove all generated files
	$(MAKE) clean-bin

.PHONY: clean-bin
clean-bin: ## Remove all generated binaries
	rm -rf bin
	rm -rf hack/tools/bin

.PHONY: clean-release
clean-release: ## Remove the release folder
	rm -rf $(RELEASE_DIR)

.PHONY: verify
verify: lint-full ## Runs all the verifier
	./hack/verify-boilerplate.sh
	./hack/verify-shellcheck.sh
	$(MAKE) verify-modules
	$(MAKE) verify-gen

.PHONY: verify-modules
verify-modules: modules ## Verifies go modules are up to date
	@if !(git diff --quiet HEAD -- go.sum go.mod hack/tools/go.mod hack/tools/go.sum); then \
		git diff; \
		echo "go module files are out of date"; exit 1; \
	fi

.PHONY: verify-gen
verify-gen: generate ## Verifies generated files are up to date
	@if !(git diff --quiet HEAD); then \
		git diff; \
		echo "generated files are out of date, run make generate"; exit 1; \
	fi
