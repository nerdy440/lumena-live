# Builds the unified Lumena API server (feed/cmd/api) — the single binary
# that serves auth, profile, feed, streaming, chat, gifts, matchmaking,
# moderation, admin, analytics, fraud, rollout, and referral (see
# feed/cmd/api/main.go's phase list). The whole repo is a Go workspace
# (go.work) of ~20 separately-versioned modules, so the build context is
# the repo root, not just feed/.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Copy the workspace file first so module resolution doesn't need every
# source file to be present yet — but go.work has no separate checksum
# file to leverage for a cheap dependency-only layer (each module's own
# go.sum already covers that), so we copy everything in one shot.
COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -o /out/lumena-api ./feed/cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app

COPY --from=build /out/lumena-api ./lumena-api
COPY migrations ./migrations

ENV PORT=8080
EXPOSE 8080

ENTRYPOINT ["./lumena-api"]
