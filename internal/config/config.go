package config

import "os"

// Config is the env-driven runtime config for katalog-api.
type Config struct {
	Addr         string
	PgURL        string
	OIDCIssuer   string
	OIDCAudience string
}

// Load reads env vars and returns a Config with sensible defaults so the
// server can come up in scaffold mode without any env.
func Load() Config {
	return Config{
		Addr:         envOr("KATALOG_API_ADDR", ":8080"),
		PgURL:        os.Getenv("KATALOG_API_PG_URL"),
		OIDCIssuer:   envOr("KATALOG_API_OIDC_ISSUER", "https://sso.example.com/realms/your-realm"),
		OIDCAudience: envOr("KATALOG_API_OIDC_AUDIENCE", "katalog"),
	}
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
