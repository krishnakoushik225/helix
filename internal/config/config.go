package config

import (
	"fmt"
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
	CORSAllowedOrigins       string  `mapstructure:"CORS_ALLOWED_ORIGINS"`
}

func Load() (*Config, error) {
	v := viper.New()

	v.SetDefault("PORT", "8080")
	v.SetDefault("ENV", "development")
	v.SetDefault("CACHE_SIMILARITY_THRESHOLD", 0.92)
	v.SetDefault("CACHE_ENABLED", false)
	v.SetDefault("RATE_LIMIT_RPM", 60)
	v.SetDefault("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:5173")

	// SetEnvKeyReplacer must be called before AutomaticEnv so the replacer is
	// active when viper resolves keys from the process environment.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetConfigFile(".env")
	v.SetConfigType("env")
	// Ignore a missing .env file — real env vars always take precedence.
	_ = v.ReadInConfig()

	v.AutomaticEnv()

	// Build Config with explicit v.Get* calls instead of v.Unmarshal.
	//
	// v.Unmarshal internally calls AllSettings(), which only iterates keys that
	// are already registered in Viper's key store (via SetDefault, ReadInConfig,
	// or BindEnv). On Fly.io there is no .env file, so secrets like DATABASE_URL
	// and REDIS_URL are never registered and Unmarshal silently leaves them as
	// zero values even though the env vars are set.
	//
	// v.GetString / v.GetBool / v.GetInt / v.GetFloat64, by contrast, always
	// walk the full lookup chain — override → flag → env → config file → default
	// — so they find any env var that AutomaticEnv can see, registered or not.
	cfg := &Config{
		Port:                     v.GetString("PORT"),
		Env:                      v.GetString("ENV"),
		DatabaseURL:              v.GetString("DATABASE_URL"),
		RedisURL:                 v.GetString("REDIS_URL"),
		OpenAIAPIKey:             v.GetString("OPENAI_API_KEY"),
		AnthropicAPIKey:          v.GetString("ANTHROPIC_API_KEY"),
		OllamaBaseURL:            v.GetString("OLLAMA_BASE_URL"),
		JWTSecret:                v.GetString("JWT_SECRET"),
		CacheEnabled:             v.GetBool("CACHE_ENABLED"),
		CacheSimilarityThreshold: v.GetFloat64("CACHE_SIMILARITY_THRESHOLD"),
		RateLimitRPM:             v.GetInt("RATE_LIMIT_RPM"),
		CORSAllowedOrigins:       v.GetString("CORS_ALLOWED_ORIGINS"),
	}

	if cfg.Env == "production" && cfg.JWTSecret == "" {
		return nil, fmt.Errorf("config: JWT_SECRET must be set in production")
	}

	return cfg, nil
}
