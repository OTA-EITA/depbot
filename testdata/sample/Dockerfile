FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN go build -o /sample .

FROM alpine:3.18
COPY --from=build /sample /sample
ENTRYPOINT ["/sample"]
