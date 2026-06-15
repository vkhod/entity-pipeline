# Multi-stage build -> two tiny static binaries (api, worker) on distroless.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/api /app/api
COPY --from=build /out/worker /app/worker
# Entrypoint is set per service via compose `command`.
