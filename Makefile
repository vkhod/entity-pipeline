.PHONY: run down build tidy test demo scale

run: ; docker compose up --build
down: ; docker compose down -v
build: ; go build ./...
tidy: ; go mod tidy
test: ; go test ./...
demo: ; ./scripts/demo.sh
scale: ; docker compose up --build --scale classification-worker=5
