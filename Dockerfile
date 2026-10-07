FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN go vet ./... && CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -ldflags="-s -w" -o /app .

FROM gcr.io/distroless/static
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
