# syntax=docker/dockerfile:experimental

FROM golang:1.13.5-alpine3.10 AS builder

RUN apk update && \
    apk add --no-cache \
        git

WORKDIR /src/zero-pod-autoscaler
COPY go.mod go.sum /src/zero-pod-autoscaler/
RUN go mod download

COPY . ./
RUN --mount=type=cache,target=/tmp/gocache GOCACHE=/tmp/gocache CGO_ENABLED=0 go build .

FROM scratch
COPY --from=builder /src/zero-pod-autoscaler/zero-pod-autoscaler /bin/zero-pod-autoscaler
ENTRYPOINT [ "/bin/zero-pod-autoscaler" ]
