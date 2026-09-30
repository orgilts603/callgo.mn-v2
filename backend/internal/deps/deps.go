// Package deps pins third-party modules in go.mod until every package that
// uses them lands. Safe to delete once the tree is fully wired.
package deps

import (
	_ "github.com/go-chi/chi/v5"
	_ "github.com/go-chi/chi/v5/middleware"
	_ "github.com/go-chi/cors"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/google/uuid"
	_ "github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/joho/godotenv"
	_ "github.com/livekit/protocol/auth"
	_ "github.com/livekit/protocol/livekit"
	_ "github.com/livekit/protocol/webhook"
	_ "github.com/livekit/server-sdk-go/v2"
	_ "github.com/rs/zerolog"
	_ "github.com/rs/zerolog/log"
	_ "github.com/stretchr/testify/require"
	_ "golang.org/x/crypto/bcrypt"
)
