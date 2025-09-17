# ---------------------------
# Load environment variables if the file exists
# ---------------------------

-include .env
export

.PHONY: run docker-build docker-tag docker-push docker-publish docker-secret deploy-dev deploy-dev-only

# ---------------------------
# Local run
# ---------------------------

run:
	mkdir -p ./data
	DATA_DIR=./data PORT=$(APP_PORT) go run main.go

# ---------------------------
# Docker Build/Push
# ---------------------------

docker-build:
	docker build --platform $(PLATFORM) \
    		-t $(IMAGE_NAME):$(VERSION) \
    		-t $(REPO)/$(IMAGE_NAME):$(VERSION) \
    		-f Dockerfile .

docker-tag:
	docker tag $(IMAGE_NAME):$(VERSION) $(REPO)/$(IMAGE_NAME):$(VERSION)

docker-push:
	docker push $(REPO)/$(IMAGE_NAME):$(VERSION)

docker-publish: docker-build docker-tag docker-push

# ---------------------------
# Kubernetes: Namespace + Docker Registry Secret
# ---------------------------

docker-secret:
	# Ensure namespace exists first
	kubectl apply -f $(DEPLOYMENT_DIR)/01-namespace.yaml
	# Create/Update the docker-registry secret
	kubectl create secret docker-registry imageregistry \
		--namespace=$(KUBE_NAMESPACE) \
		--docker-server=$(DOCKER_SERVER) \
		--docker-username=$(DOCKER_USERNAME) \
		--docker-password=$(DOCKER_PASSWORD) \
		--docker-email=$(DOCKER_EMAIL) \
		--dry-run=client -o yaml | kubectl apply -f -

# ---------------------------
# Kubernetes Deploy
# ---------------------------

# Runs docker-secret first (namespace + secret), then applies the rest of the manifests
deploy-dev: docker-secret
	kubectl apply -f $(DEPLOYMENT_DIR)

# If you ever need to apply manifests WITHOUT touching the secret/namespace
deploy-dev-only:
	kubectl apply -f $(DEPLOYMENT_DIR)


# ---------------------------
# Kubernetes Rollout Restart
# ---------------------------

rollout:
	kubectl rollout restart deployment $(DEPLOYMENT_NAME) -n $(KUBE_NAMESPACE)
	kubectl rollout status deployment $(DEPLOYMENT_NAME) -n $(KUBE_NAMESPACE)

hot: docker-publish deploy-dev rollout