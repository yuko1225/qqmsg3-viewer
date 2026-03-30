package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"qqviewer/internal/avatar"
	"qqviewer/internal/config"
	"qqviewer/internal/db"
	"qqviewer/internal/handler"
)

func main() {
	configPath := flag.String("config", "config.conf", "path to config file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Validate DB path
	if cfg.Database.Path == "" {
		log.Fatal("database.path must be set in config.toml")
	}
	if _, err := os.Stat(cfg.Database.Path); os.IsNotExist(err) {
		log.Fatalf("database file not found: %s", cfg.Database.Path)
	}

	// Open database
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	tables := database.Tables()
	log.Printf("Loaded %d chat tables from database", len(tables))

	// Ensure cache dir exists
	if err := os.MkdirAll(cfg.Cache.Dir, 0755); err != nil {
		log.Fatalf("create cache dir: %v", err)
	}

	// Initialize avatar cache
	avatarCache, err := avatar.NewCache(cfg.Cache.Dir, cfg.Cache.AvatarRateLimit)
	if err != nil {
		log.Fatalf("init avatar cache: %v", err)
	}
	defer avatarCache.Close()

	// Load HTML templates
	tmplDir := "templates"
	tmplPattern := filepath.Join(tmplDir, "*.html")
	tmpls, err := template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}).ParseGlob(tmplPattern)
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}

	// Build HTTP handler
	h := handler.New(cfg, database, avatarCache, tmpls)
	router := h.Router()

	addr := cfg.Server.Listen
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	log.Printf("QQ Viewer starting at http://%s", addr)
	log.Printf("Database: %s", cfg.Database.Path)
	log.Printf("Cache dir: %s", cfg.Cache.Dir)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server: %v", err)
	}
}
