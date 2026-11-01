package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
)

type proxyKeyRequest struct {
	ID       string                   `json:"id"`
	Provider contract.AccountProvider `json:"provider"`
	Key      string                   `json:"key"`
}

func validProxyKeyID(id string) bool {
	if !strings.HasPrefix(id, "dyna-key-") || len(id) != len("dyna-key-")+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "dyna-key-"))
	return err == nil
}

func proxyKeyRoutes(router *gin.Engine, service **cliproxy.Service, credentials *proxyStore, managementHash string) {
	dyna := router.Group("/dyna", func(c *gin.Context) {
		password := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if bcrypt.CompareHashAndPassword([]byte(managementHash), []byte(password)) != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	})
	dyna.POST("/keys", func(c *gin.Context) {
		var req proxyKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil || !validProxyKeyID(req.ID) || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
			c.Status(http.StatusBadRequest)
			return
		}
		for _, provider := range model.KeyProviders() {
			if provider.Provider != req.Provider {
				continue
			}
			name := string(provider.Provider)
			// The proxy SDK defines provider metadata as map[string]any.
			auth := &coreauth.Auth{
				ID:         req.ID,
				Provider:   "openai-compatible-" + name,
				Label:      name,
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"api_key": req.Key, "auth_kind": "apikey", "base_url": provider.URL, "compat_name": name, "provider_key": "openai-compatible-" + name},
				Metadata:   map[string]any{"type": "openai-compatibility"},
			}
			if err := (*service).RegisterDynaCredential(c.Request.Context(), auth); err != nil {
				c.Status(http.StatusServiceUnavailable)
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		c.Status(http.StatusBadRequest)
	})

	dyna.POST("/oauth/import", func(c *gin.Context) {
		var req struct {
			ID         string                   `json:"id"`
			Provider   contract.AccountProvider `json:"provider"`
			ExpiresAt  time.Time                `json:"expiresAt"`
			Credential string                   `json:"credential"`
		}
		if c.ShouldBindJSON(&req) != nil || !strings.HasPrefix(req.ID, "dyna-oauth-") || !validProxyKeyID("dyna-key-"+strings.TrimPrefix(req.ID, "dyna-oauth-")) || (req.Provider != contract.ProviderKimi && req.Provider != contract.ProviderXAI) || req.ExpiresAt.Unix() <= 0 || len(req.Credential) > 16384 {
			c.Status(http.StatusBadRequest)
			return
		}
		var token struct {
			Access   string `json:"access_token"`
			Refresh  string `json:"refresh_token"`
			Domain   string `json:"domain"`
			DeviceID string `json:"device_id"`
		}
		if json.Unmarshal([]byte(req.Credential), &token) != nil || strings.TrimSpace(token.Access) == "" || strings.TrimSpace(token.Refresh) == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		if token.Domain != "" && (req.Provider != contract.ProviderKimi || (token.Domain != "kimi.com" && token.Domain != "kimi.ai")) {
			c.Status(http.StatusBadRequest)
			return
		}
		// The proxy SDK uses provider-specific metadata maps for credentials.
		auth := &coreauth.Auth{ID: req.ID, Provider: string(req.Provider), Label: string(req.Provider), Status: coreauth.StatusActive, Metadata: map[string]any{"type": string(req.Provider), "access_token": token.Access, "refresh_token": token.Refresh, "expired": req.ExpiresAt.UTC().Format(time.RFC3339Nano), "token_type": "Bearer"}}
		if token.Domain != "" {
			auth.Metadata["domain"] = token.Domain
		}
		if req.Provider == contract.ProviderKimi && token.DeviceID != "" {
			auth.Metadata["device_id"] = token.DeviceID
		}
		if (*service).RegisterDynaCredential(c.Request.Context(), auth) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	dyna.DELETE("/keys/:id", func(c *gin.Context) {
		id := c.Param("id")
		if !validProxyKeyID(id) {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := (*service).DeleteDynaCredential(c.Request.Context(), id); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	dyna.GET("/credentials", func(c *gin.Context) {
		c.JSON(http.StatusOK, struct {
			Credentials []cliproxy.DynaCredential `json:"credentials"`
		}{(*service).DynaCredentials()})
	})
	dyna.POST("/credentials/activate", func(c *gin.Context) {
		var req struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if c.ShouldBindJSON(&req) != nil || req.ID == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		bound, err := credentials.bound(req.ID)
		if err != nil || !bound || (*service).ActivateDynaCredential(c.Request.Context(), req.ID, req.Model) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	dyna.POST("/credentials/remove", func(c *gin.Context) {
		var req struct {
			ID string `json:"id"`
		}
		if c.ShouldBindJSON(&req) != nil || req.ID == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		if (*service).DeleteDynaCredential(c.Request.Context(), req.ID) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
