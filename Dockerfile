FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
# ponytail: go.sum se genera en el build (no había Go local); commitealo cuando corras `go mod tidy` a mano.
RUN go mod tidy && go vet ./... && CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -ldflags="-s -w" -o /app .

FROM gcr.io/distroless/static
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
