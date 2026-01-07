package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/apikeys"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/billing"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/httpapi"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/policy"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/quota"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/ratelimit"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/usage"
)

func main() {
	var (
		registryPath  = flag.String("registry", os.Getenv("REGISTRY_PATH"), "Path to registry.yaml")
		listenAddress = flag.String("listen", envDefault("LISTEN", ":8080"), "Listen address")
	)
	flag.Parse()

	if *registryPath == "" {
		log.Fatal("missing registry path (set --registry or REGISTRY_PATH)")
	}

	reg, err := registry.Load(*registryPath)
	if err != nil {
		log.Fatalf("load registry: %v", err)
	}
	reg.MergeDefaultAliases()

	authCfg := auth.LoadConfigFromEnv()
	auther, err := auth.New(context.Background(), authCfg)
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}

	policyCfg := policy.LoadConfigFromEnv(reg)
	pol := policy.New(policyCfg)

	// Initialize rate limiter
	rateLimitCfg := ratelimit.LoadConfigFromEnv()
	rateLimiter, err := ratelimit.New(rateLimitCfg)
	if err != nil {
		log.Fatalf("ratelimit init: %v", err)
	}
	defer rateLimiter.Close()

	var rateLimitAdapter *ratelimit.GatewayAdapter
	if rateLimiter.Enabled() {
		rateLimitAdapter = ratelimit.NewGatewayAdapter(rateLimiter)
	}

	// Initialize usage tracker
	usageCfg := usage.LoadConfigFromEnv()
	usageTracker, err := usage.New(usageCfg)
	if err != nil {
		log.Fatalf("usage tracker init: %v", err)
	}
	defer usageTracker.Close()

	// Initialize quota manager
	quotaCfg := quota.LoadConfigFromEnv()
	quotaManager, err := quota.New(quotaCfg)
	if err != nil {
		log.Fatalf("quota manager init: %v", err)
	}
	defer quotaManager.Close()

	// Initialize API key manager
	apikeysCfg := apikeys.LoadConfigFromEnv()
	apikeysManager, err := apikeys.New(apikeysCfg)
	if err != nil {
		log.Fatalf("apikeys manager init: %v", err)
	}
	defer apikeysManager.Close()

	// Initialize billing webhook sender
	billingCfg := billing.LoadConfigFromEnv()
	webhookSender := billing.NewWebhookSender(billingCfg)
	defer webhookSender.Close()

	// Connect billing webhooks to quota manager
	quotaManager.SetWebhookSender(webhookSender)

	api := httpapi.New(httpapi.Config{
		Registry:      reg,
		Authenticator: auther,
		Policy:        pol,
		RateLimiter:   rateLimitAdapter,
		APIKeys:       apikeysManager,
		Quotas:        quotaManager,
		Usage:         usageTracker,
	})
	srv := &http.Server{
		Addr:              *listenAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("fi-mcp-gateway listening on %s", *listenAddress)
	log.Printf("registry: %s (%d servers)", *registryPath, len(reg.Servers))
	log.Printf("auth: mode=%s required=%t", authCfg.Mode, authCfg.Required)
	log.Printf("ratelimit: enabled=%t store=%s", rateLimitCfg.Enabled, rateLimitCfg.Store)
	log.Printf("usage: enabled=%t store=%s", usageCfg.Enabled, usageCfg.Store)
	log.Printf("quota: enabled=%t store=%s", quotaCfg.Enabled, quotaCfg.Store)
	log.Printf("apikeys: enabled=%t store=%s", apikeysCfg.Enabled, apikeysCfg.Store)
	log.Printf("billing: enabled=%t", billingCfg.Enabled)
	log.Printf("health: http://localhost%s/health", addrForLog(*listenAddress))

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func addrForLog(addr string) string {
	if addr == "" {
		return ""
	}
	if addr[0] == ':' {
		return addr
	}
	return fmt.Sprintf(":%s", addr)
}
