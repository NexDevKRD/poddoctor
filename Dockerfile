FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o manager \
    ./cmd/main.go

FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.source="https://github.com/chenar/poddoctor" \
      org.opencontainers.image.description="Diagnoses why pods are stuck in CrashLoopBackOff/ImagePullBackOff" \
      org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /

COPY --from=builder /workspace/manager .

USER 65532:65532

ENTRYPOINT ["/manager"]
