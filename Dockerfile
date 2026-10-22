FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /depbot ./cmd/depbot

# distroless/static runs as a non-root user and ships no shell.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /depbot /depbot
ENTRYPOINT ["/depbot"]
