package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Port                     string  `mapstructure:"PORT"`
	DatabaseURL              string  `mapstructure:"DATABASE_URL"`
	RedisURL                 string  `mapstructure:"REDIS_URL"`
	AnthropicAPIKey          string  `mapstructure:"ANTHROPIC_API_KEY"`
	OpenAIAPIKey             string  `mapstructure:"OPENAI_API_KEY"`
	OllamaBaseURL            string  `mapstructure:"OLLAMA_BASE_URL"`
	JWTSecret                string  `mapstructure:"JWT_SECRET"`
	CacheSimilarityThreshold float64 `mapstructure:"CACHE_SIMILARITY_THRESHOLD"`
	CacheEnabled             bool    `mapstructure:"CACHE_ENABLED"`
	RateLimitRPM             int     `mapstructure:"RATE_LIMIT_RPM"`
	Env                      string  `mapstructure:"ENV"`
	// CORSAllowedOrigins is a comma-separated list of allowed CORS origins.
	// Defaults to the two standard Vite/React dev-server addresses.
	CORSAllowedOrigins string `mapstructure:"CORS_ALLOWED_ORIGINS"`
}

func Load() (*Config, error) {
	v := viper.New()

	v.SetDefault("PORT", "8080")
	v.SetDefault("ENV", "development")
	v.SetDefault("CACHE_SIMILARITY_THRESHOLD", 0.92)
	v.SetDefault("CACHE_ENABLED", false)
	v.SetDefault("RATE_LIMIT_RPM", 60)
	v.SetDefault("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:5173")

	// SetEnvKeyReplacer must be registered before AutomaticEnv so the replacer
	// is active when viper resolves any key from the process environment.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetConfigFile(".env")
	v.SetConfigType("env")
	// Ignore a missing .env — real env vars always take precedence.
	_ = v.ReadInConfig()

	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Belt-and-suspenders: viper.Unmarshal only visits keys it already knows
	// about (from SetDefault, ReadInConfig, or BindEnv). On Fly.io there is no
	// .env file, so secrets such as JWT_SECRET have no entry in the registry and
	// are silently left as zero values even though the env var is set. Read them
	// directly from the environment so they are never missed.
	if s := os.Getenv("JWT_SECRET"); s != "" {
		cfg.JWTSecret = s
	}

	if cfg.Env == "production" && cfg.JWTSecret == "" {
		return nil, fmt.Errorf("config: JWT_SECRET must be set in production")
	}

	return &cfg, nil
}
