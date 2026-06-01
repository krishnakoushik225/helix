package config

import (
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
}

func Load() (*Config, error) {
	v := viper.New()

	v.SetDefault("PORT", "8080")
	v.SetDefault("ENV", "development")
	v.SetDefault("CACHE_SIMILARITY_THRESHOLD", 0.92)
	v.SetDefault("CACHE_ENABLED", false)
	v.SetDefault("RATE_LIMIT_RPM", 60)

	v.SetConfigFile(".env")
	v.SetConfigType("env")
	// ignore missing .env — env vars take precedence anyway
	_ = v.ReadInConfig()

	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
