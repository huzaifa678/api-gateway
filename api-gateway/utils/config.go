package utils

import (
	"log"
	"strings"

	"github.com/spf13/viper"
)

type CircuitBreakerConfig struct {
	TimeoutMs       int `mapstructure:"timeoutMs"`
	ErrorThreshold  int `mapstructure:"errorThreshold"`
	ResetTimeoutMs  int `mapstructure:"resetTimeoutMs"`
}

type CORSConfig struct {
	AllowedOrigins []string `mapstructure:"allowedOrigins"`
}

type Config struct {
	App struct {
		Name string `mapstructure:"name"`
		Env  string `mapstructure:"env"`
		Port string `mapstructure:"port"`
	} `mapstructure:"app"`

	Jwt struct {
		Secret string `mapstructure:"secret"`
	}

	Redis struct {
		URL string `mapstructure:"url"`
	}

	Keycloak struct {
		JWKSURL string `mapstructure:"jwksURL"`
	}

	Services struct {
		Auth struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"auth"`

		Subscription struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"subscription"`

		Billing struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"billing"`
		
	} `mapstructure:"services"`

	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuitBreaker"`
	CORS           CORSConfig           `mapstructure:"cors"`
}

func Load() *Config {
	v := viper.New()

	v.SetConfigName("app")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")

	v.SetEnvPrefix("GATEWAY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("app.port", "9000")

	if err := v.ReadInConfig(); err != nil {
		log.Println("no config file found, relying on env vars")
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		log.Fatalf("unable to decode config: %v", err)
	}

	validate(cfg)
	return &cfg
}
