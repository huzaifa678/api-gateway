package utils

import "log"

func validate(cfg Config) {
	if cfg.Services.Auth.URL == "" {
		log.Fatal("services.auth.url is required")
	}
}
