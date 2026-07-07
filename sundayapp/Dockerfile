# Build stage: modernc.org/sqlite is pure Go, so CGO stays off and the
# binary is fully static.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/sunday-app .

# Runtime stage: alpine (not scratch) so `kubectl exec` shell demos work.
FROM alpine:3.20
RUN addgroup -S sunday && adduser -S sunday -G sunday \
    && mkdir -p /data && chown sunday:sunday /data
COPY --from=build /out/sunday-app /usr/local/bin/sunday-app
USER sunday
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sunday-app"]
