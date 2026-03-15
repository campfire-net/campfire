FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /cf ./cmd/cf

FROM alpine:3.19
COPY --from=build /cf /usr/local/bin/cf
ENTRYPOINT ["cf"]
