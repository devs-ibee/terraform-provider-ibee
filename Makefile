.PHONY: build fmt test test-terraform vet docs check
build:
	go build -trimpath -o bin/terraform-provider-ibee .
fmt:
	gofmt -w main.go internal
	terraform fmt -recursive examples
test:
	go test -race ./...
test-terraform:
	IBEE_TF_TEST=1 go test -count=1 -run TestTerraform -v ./internal/provider
vet:
	go vet ./...
docs:
	python3 scripts/generate_docs.py
check:
	@test -z "$$(gofmt -l main.go internal)"
	terraform fmt -check -recursive examples
	go vet ./...
	go test ./...
	python3 scripts/generate_docs.py --check
